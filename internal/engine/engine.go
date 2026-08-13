// Package engine downloads a torrent in this process.
//
// It is the second implementation of download.TorrentClient, beside
// internal/qbit, and the default one (docs/decisions.md D22). The interface did
// not change to accommodate it: the four methods do the same four things, every
// test in internal/download runs on a fake, and internal/api has never seen a
// torrent client of any kind.
//
// # Why it is here at all
//
// A VPN curator can guarantee has to be a VPN curator controls, and that means
// the socket the peer bytes leave through has to belong to this process
// (docs/decisions.md D27). Everything else this buys — one filesystem namespace,
// no cookie session, no confirm-by-hash dance, no orphan torrents — is a
// consequence.
//
// # CGO_ENABLED=0 is load-bearing
//
// With cgo ON, anacrolix/torrent pulls go-libutp, go-llsqlite/crawshaw and
// crawshaw/c: a C uTP implementation and a second SQLite beside D4's pure-Go
// one. utp_go.go is `//go:build !cgo` and selects the pure-Go anacrolix/utp
// instead, which is also what makes the tunnel's netstack wiring possible at
// all. Go disables cgo by itself when cross-compiling, so this repo's usual
// `GOOS=linux GOARCH=arm64 go build ./...` will never catch a regression here —
// the Dockerfile has to set it explicitly (T47).
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"

	anacrolix "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/utp"

	"github.com/DulsaraNethmin/curator/internal/torrent"
)

// Network is the network the engine's traffic goes through. A nil Network means
// the host's own stack; internal/vpn's tunnel is the other implementation (T37).
//
// The engine takes an interface rather than importing internal/vpn, because a
// package that owns a socket should not also own a VPN — and because the tests
// below hand it plain loopback, which is how the whole binding is exercised
// without a tunnel.
type Network interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
	ListenPacket(ctx context.Context, network, address string) (net.PacketConn, error)
}

// Defaults. MaxConns and unverifiedBytes are the two memory levers: the spike
// measured peak RSS at ~1:1 with payload size (822 MB for 755 MB) on a Pi that
// has 8 GB and is also running Jellyfin, so they are set below anacrolix's own
// defaults of 50 conns and 64 MiB.
const (
	DefaultMaxConns = 24

	unverifiedBytes = 32 << 20

	// metainfoDir sits inside the data directory, dot-prefixed so that
	// directory walkers skip it. It holds what anacrolix does not persist.
	metainfoDir = ".curator"

	dirMode = 0o755
)

// Engine is a torrent client that runs here. It satisfies
// download.TorrentClient; the zero value is not usable, call New.
type Engine struct {
	client   *anacrolix.Client
	dataDir  string
	category string
	log      *slog.Logger

	// socket is the shared uTP/DHT socket when a Network was supplied, and nil
	// when the client is using the host's stack and owns its own sockets.
	socket *utp.Socket

	// ctx bounds the per-torrent goroutines. They wait on metadata that may
	// never arrive, so they need an owner that dies with the engine rather than
	// a lifetime of their own.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Config is what New needs. Only DataDir is required.
type Config struct {
	DataDir  string
	Category string
	MaxConns int
	Network  Network
	Log      *slog.Logger

	// ListenPort is where incoming peer connections are accepted. Zero means
	// an ephemeral port, which is the default and is right for almost
	// everybody: behind a tunnel with no port forwarding nothing can reach us
	// anyway, and anacrolix's own default of a fixed 42069 is both a
	// fingerprint and a second instance on the same host failing to start.
	// Set it when a VPN provider forwards a port.
	ListenPort int
}

// New starts a torrent client.
//
// With a Network, the client is built so that it CANNOT open a socket of its
// own: DisableTCP, DisableUTP and NoDHT leave it with only the dialers and
// listeners it is given, which is what makes the kill switch structural rather
// than a setting somebody could turn off. The spike measured exactly that — zero
// OS sockets opened by the client — and it is destroyed by one code path
// falling back to net.Dial "just for trackers".
func New(cfg Config) (*Engine, error) {
	if strings.TrimSpace(cfg.DataDir) == "" {
		return nil, errors.New("engine: DataDir is required")
	}
	dataDir, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("engine: data directory %q: %w", cfg.DataDir, err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, metainfoDir), dirMode); err != nil {
		return nil, fmt.Errorf("engine: data directory %q: %w", dataDir, err)
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.MaxConns <= 0 {
		cfg.MaxConns = DefaultMaxConns
	}

	cc := anacrolix.NewDefaultClientConfig()
	cc.DataDir = dataDir
	cc.Seed = true
	cc.ListenPort = cfg.ListenPort
	cc.EstablishedConnsPerTorrent = cfg.MaxConns
	cc.HalfOpenConnsPerTorrent = cfg.MaxConns / 2
	cc.MaxUnverifiedBytes = unverifiedBytes
	// anacrolix logs at its own level through a logger of its own design. Its
	// output is not curator's log format and there is nothing in it a user of
	// this app can act on, so it is discarded rather than interleaved.
	cc.Logger = cc.Logger.FilterLevel(anacrolixQuiet)

	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{
		dataDir:  dataDir,
		category: cfg.Category,
		log:      cfg.Log,
		ctx:      ctx,
		cancel:   cancel,
	}

	// The socket has to exist before the client, and the client before the
	// dialers can be attached to it, so the tunnel is bound in two halves.
	if cfg.Network != nil {
		socket, err := listenTunnel(ctx, cfg.Network)
		if err != nil {
			cancel()
			return nil, err
		}
		e.socket = socket
		bindConfig(ctx, cc, cfg.Network)
	}

	client, err := anacrolix.NewClient(cc)
	if err != nil {
		if e.socket != nil {
			e.socket.Close()
		}
		cancel()
		return nil, fmt.Errorf("engine: starting the torrent client: %w", err)
	}
	e.client = client

	if e.socket != nil {
		if err := e.attachNetwork(); err != nil {
			client.Close()
			e.socket.Close()
			cancel()
			return nil, err
		}
	}

	e.log.Info("torrent engine started", "data_dir", dataDir, "max_conns", cfg.MaxConns,
		"tunnelled", cfg.Network != nil)
	return e, nil
}

// AddMagnet adds one magnet and starts downloading it.
//
// Unlike qBittorrent's add, this one is authoritative: the torrent exists in
// this process when it returns, so there is nothing to confirm afterwards. The
// metadata still has to arrive from the swarm before any payload byte can be
// requested, which is what the goroutine below waits for.
func (e *Engine) AddMagnet(_ context.Context, magnet, category string) error {
	if strings.TrimSpace(magnet) == "" {
		return errors.New("engine: empty magnet")
	}
	if err := e.assertCategory(category); err != nil {
		return err
	}

	// Parsed here, before the client sees it. A magnet with no `xt=urn:btih:`
	// parses cleanly and then PANICS inside AddTorrentOpt on the zero info hash
	// — measured, not feared. Dispatch rejects those already, but T36's boot
	// resume re-adds magnets straight out of the database, and a library panic
	// is not something a caller can recover from three layers up.
	parsed, err := metainfo.ParseMagnetUri(magnet)
	if err != nil {
		return fmt.Errorf("engine: %q is not a magnet: %w", clip(magnet), err)
	}
	if parsed.InfoHash.IsZero() {
		return fmt.Errorf("engine: %q carries no info hash", clip(magnet))
	}

	t, err := e.client.AddMagnet(magnet)
	if err != nil {
		return fmt.Errorf("engine: adding magnet: %w", err)
	}
	e.start(t)
	return nil
}

// clip keeps a magnet's tracker list out of an error message. Only the front of
// it identifies anything.
func clip(magnet string) string {
	const limit = 72
	if len(magnet) <= limit {
		return magnet
	}
	return magnet[:limit] + "…"
}

// start waits for a torrent's metadata and then does the three things that need
// it: cap its peers, persist the info dict, and ask for every piece.
//
// It is a goroutine because metadata arrives from strangers and may never
// arrive at all. It is owned by the engine's context so that it dies with
// Close, rather than being a goroutine with a life of its own — the rule the
// poller and the search cache already follow.
func (e *Engine) start(t *anacrolix.Torrent) {
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		select {
		case <-t.GotInfo():
		case <-t.Closed():
			return
		case <-e.ctx.Done():
			return
		}

		if err := e.persistMetainfo(t); err != nil {
			// Not fatal: the download works, and only an offline restart is
			// poorer for it. Said once, with the reason.
			e.log.Warn("could not persist the metainfo; this torrent will need the swarm to resume",
				"hash", hashOf(t), "err", err)
		}
		t.DownloadAll()
	}()
}

// Torrents lists what this engine holds.
//
// The category filter is honoured but cannot fail: this engine only ever holds
// torrents curator added, so ownership is structural rather than a string
// comparison. That is a strengthening of D13's guarantee — the importer can
// never touch a torrent somebody added by hand, because there is no way for
// anybody to add one.
func (e *Engine) Torrents(_ context.Context, category string) ([]torrent.Torrent, error) {
	if category != "" && !strings.EqualFold(category, e.category) {
		return nil, nil
	}

	held := e.client.Torrents()
	if len(held) == 0 {
		return nil, nil
	}
	torrents := make([]torrent.Torrent, 0, len(held))
	for _, t := range held {
		torrents = append(torrents, e.describe(t))
	}
	return torrents, nil
}

// TorrentByHash looks one torrent up. A nil Torrent with a nil error means this
// engine does not have it — absence is a normal answer, not a failure.
func (e *Engine) TorrentByHash(_ context.Context, hash string) (*torrent.Torrent, error) {
	ih, err := parseHash(hash)
	if err != nil {
		return nil, err
	}
	t, ok := e.client.Torrent(ih)
	if !ok {
		return nil, nil
	}
	found := e.describe(t)
	return &found, nil
}

// DeleteTorrent drops a torrent and, when asked, removes the files it wrote.
//
// D19 narrowed deletion to "curator only ever deletes a file it created
// itself", and that guarantee is stronger here than it was with qBittorrent,
// because now curator really did create it. The containment check is kept
// anyway: the path is built from a name that came out of a torrent, which is to
// say from a stranger.
//
// A torrent that is already gone is success. Delete gets retried after a
// partial failure, and a retry that fails because the work was already done is
// a retry nobody can complete.
func (e *Engine) DeleteTorrent(ctx context.Context, hash, requireCategory string, deleteFiles bool) error {
	ih, err := parseHash(hash)
	if err != nil {
		return err
	}
	t, ok := e.client.Torrent(ih)
	if !ok {
		return nil
	}
	if requireCategory != "" && !strings.EqualFold(e.category, requireCategory) {
		return fmt.Errorf("engine: %s is in category %q, not %q: %w",
			ih.HexString(), e.category, requireCategory, torrent.ErrWrongCategory)
	}

	// The path is read before the drop, because a dropped torrent no longer
	// answers questions about itself.
	content := e.contentPath(t)
	t.Drop()

	if !deleteFiles {
		return nil
	}
	if content != "" {
		if err := e.assertInsideData(content); err != nil {
			return err
		}
		if err := os.RemoveAll(content); err != nil {
			return fmt.Errorf("engine: removing %s: %w", content, err)
		}
	}
	if err := os.Remove(e.metainfoPath(ih)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("engine: removing the metainfo for %s: %w", ih.HexString(), err)
	}
	return nil
}

// Close stops the client and waits for the goroutines it started. Seeding ends
// with the process, which is the whole seeding policy this phase has.
func (e *Engine) Close() error {
	e.cancel()
	errs := e.client.Close()
	e.wg.Wait()
	if e.socket != nil {
		// Closed after the client, which is still writing through it until it
		// is not.
		if err := e.socket.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// describe is the translation into curator's vocabulary. There is no 23-entry
// state map here and there should not be: qBittorrent's states describe a
// program with queues, priorities and a scheduler, while this one has three
// answers to "can I have the file yet".
func (e *Engine) describe(t *anacrolix.Torrent) torrent.Torrent {
	out := torrent.Torrent{
		Hash:        hashOf(t),
		Name:        t.Name(),
		Category:    e.category,
		ContentPath: e.contentPath(t),
	}

	info := t.Info()
	switch {
	case info == nil:
		// No info dict yet: the metadata is still being fetched from the swarm,
		// so nothing about the payload is known — not its size, not its name on
		// disk, not how much of it we have.
		out.State = torrent.StateQueued
	case t.Complete().Bool():
		out.State = torrent.StateCompleted
		out.Progress = 1
	default:
		out.State = torrent.StateDownloading
		if length := t.Length(); length > 0 {
			out.Progress = float64(t.BytesCompleted()) / float64(length)
		}
	}

	// StateFailed is deliberately unreachable. This engine has no equivalent of
	// qBittorrent's `error` or `missingFiles`: a torrent it cannot make
	// progress on is not failed, it is stalled — nobody is seeding it — and
	// saying `failed` would tell somebody their download died when what
	// happened is that they picked an unpopular release. T36 owns that word.
	return out
}

// contentPath is where the payload is, in curator's own filesystem, because
// there is only one. The file for a single-file torrent, the folder for a
// multi-file one — which is what library.FindFeature expects either way, and
// the same convention qBittorrent's content_path follows.
func (e *Engine) contentPath(t *anacrolix.Torrent) string {
	info := t.Info()
	if info == nil {
		return ""
	}
	return filepath.Join(e.dataDir, info.BestName())
}

func (e *Engine) metainfoPath(ih metainfo.Hash) string {
	return filepath.Join(e.dataDir, metainfoDir, torrent.NormalizeHash(ih.HexString())+".torrent")
}

// persistMetainfo writes the info dict beside the payload.
//
// anacrolix persists the payload and a piece-completion database but never the
// info dict, so re-adding by magnet needs a metadata round trip from the swarm
// — 3.2 s when there are peers, and forever when there are not. This file is
// what lets a restart resume with the network down (T36).
//
// Written to a temporary file and renamed, because a half-written metainfo that
// survives a crash is worse than none: it would parse as corrupt every time.
func (e *Engine) persistMetainfo(t *anacrolix.Torrent) error {
	path := e.metainfoPath(t.InfoHash())
	tmp, err := os.CreateTemp(filepath.Dir(path), ".metainfo-*")
	if err != nil {
		return err
	}
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name()) // no-op once renamed
	}()

	mi := t.Metainfo()
	if err := mi.Write(tmp); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// assertInsideData refuses a path that has escaped the data directory. The same
// check importer.assertInsideLibrary makes, for the same reason: the last
// component came from a torrent's own metadata.
func (e *Engine) assertInsideData(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(e.dataDir, absolute)
	if err != nil || rel == "." || rel == ".." ||
		strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("engine: %s is not inside the data directory %s", path, e.dataDir)
	}
	return nil
}

// assertCategory catches a wiring mistake rather than a user one: this engine
// has exactly one category, so a caller passing another has been configured
// against a different client.
func (e *Engine) assertCategory(category string) error {
	if category == "" || strings.EqualFold(category, e.category) {
		return nil
	}
	return fmt.Errorf("engine: asked to add to category %q, but this engine is %q", category, e.category)
}

func hashOf(t *anacrolix.Torrent) string {
	return torrent.NormalizeHash(t.InfoHash().HexString())
}

func parseHash(hash string) (metainfo.Hash, error) {
	var ih metainfo.Hash
	hash = strings.TrimSpace(hash)
	if hash == "" {
		return ih, errors.New("engine: look up a torrent by an empty hash")
	}
	if err := ih.FromHexString(strings.ToLower(hash)); err != nil {
		return ih, fmt.Errorf("engine: %q is not a 40-character info hash: %w", hash, err)
	}
	return ih, nil
}
