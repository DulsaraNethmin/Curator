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
	"time"

	dht "github.com/anacrolix/dht/v2"
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

	// LookupHost resolves a name on this network's own resolver.
	//
	// Dialing never needed it — a hostname handed to DialContext is resolved
	// inside the tunnel already, which is what bindConfig's comment below means
	// by "passed through rather than resolved here". The DHT bootstrap is the
	// one caller that has only names and no dial to hang them on: it has to turn
	// eight host:port strings into addresses BEFORE anything is dialled, and
	// anacrolix's default does that on the host's resolver.
	//
	// It is a third method on this interface rather than a resolved list handed
	// in by cmd/curator because the answer has to be LAZY. A tunnel that has not
	// handshaken yet cannot resolve anything, and start-up is exactly when it
	// has not; the DHT asks at bootstrap, which is later and is when the tunnel
	// is up. It also makes a fake Network the whole seam a test needs.
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// Defaults. MaxConns and unverifiedBytes are the two memory levers: the spike
// measured peak RSS at ~1:1 with payload size (822 MB for 755 MB) on a Pi that
// has 8 GB and is also running Jellyfin, so they are set below anacrolix's own
// defaults of 50 conns and 64 MiB.
const (
	DefaultMaxConns = 24

	// DefaultStallAfter is how long a torrent may sit at the same byte count
	// before it stops calling itself queued. Metadata arrived in 3.2 s in the
	// spike and a healthy download moves every second, so five minutes is not a
	// tight rope — it is long enough that saying "stalled" means it.
	DefaultStallAfter = 5 * time.Minute

	// rateStaleAfter is how long a computed rate outlives the bytes that
	// produced it. anacrolix reports cumulative counters and no rate at all, so
	// a rate here is a delta between two observations that saw the byte count
	// MOVE — and with nothing moving there is no new delta to take, only an
	// increasingly old one to keep repeating.
	//
	// Thirty seconds, off a number already in this file: DefaultStallAfter's
	// note says "a healthy download moves every second", and the poll is five to
	// ten. So anything alive refreshes this several times over, and a torrent
	// that quietly stops reads 0 B/s within half a minute and `stalled` at five
	// minutes — two honest signals, in that order.
	rateStaleAfter = 30 * time.Second

	// rateFloor is the shortest interval a rate may be computed over.
	//
	// Two callers now ask for the torrent list at different cadences — the
	// poller on DOWNLOAD_POLL_INTERVAL and GET /api/downloads on whatever the
	// Activity screen is doing — so two observations can land milliseconds
	// apart. A megabyte across 40 ms is a true instantaneous rate and a useless
	// one: it reads as 25 MB/s on a 3 MB/s download. Below this the previous
	// rate is carried rather than recomputed.
	rateFloor = time.Second

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

	// network is kept for ONE thing: resolving udp:// tracker names before
	// anacrolix sees them (see resolveTrackers). Everything else the engine
	// needs from it was installed on the ClientConfig at build time, which is
	// why this field did not exist until a packet capture found the one path
	// that could not be closed that way. Binding deliberately does NOT read it
	// — that answer has to come off the socket, or it is a claim rather than a
	// reading.
	network Network

	// ctx bounds the per-torrent goroutines. They wait on metadata that may
	// never arrive, so they need an owner that dies with the engine rather than
	// a lifetime of their own.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// stallAfter is how long a torrent may make no progress before it says so.
	stallAfter time.Duration
	// now is a seam for the stall tests, which must not sleep for minutes.
	now func() time.Time

	// mu guards the two maps below. Both are read on every poll tick, which is
	// the only reason they are maps rather than fields on a wrapper type.
	mu sync.Mutex
	// unchecked holds torrents whose completeness curator has not established
	// in this process: newly added, or being re-hashed right now. They report
	// `queued`, which is what keeps the importer away from a payload that has
	// not been vouched for.
	unchecked map[string]bool
	// progress remembers when each torrent last gained a byte, which is the
	// whole of stall detection.
	progress map[string]mark

	// heldReason is why downloads are stopped, or "" when they are not. See
	// Hold: it is a string rather than a bool because the reason is the whole
	// point — a held row has to say why on the Activity screen instead of
	// blaming the swarm.
	heldReason string
	// heldConns is each torrent's own connection limit from before the hold, so
	// Release restores what was there rather than the global default.
	heldConns map[string]int
}

// mark is one torrent's last observed progress, and whether the stall it is in
// has already been said out loud.
//
// `since` is when the byte count last MOVED, not when it was last looked at —
// which is what makes it a stall clock, and what makes the interval between two
// moves a real measured rate rather than an assumed one.
type mark struct {
	bytes    int64
	since    time.Time
	reported bool

	// rate is bytes per second between the last two observations that saw the
	// count move. It is carried forward across observations that saw no
	// movement, and `describe` stops reporting it once `since` is older than
	// rateStaleAfter — a rate nobody has re-measured for half a minute is a
	// number about the past.
	rate int64
}

// Config is what New needs. Only DataDir is required.
type Config struct {
	DataDir  string
	Category string
	MaxConns int
	Network  Network
	Log      *slog.Logger

	// StallAfter is how long a torrent may make no progress at all before it
	// reports itself stalled instead of queued. Zero takes DefaultStallAfter.
	StallAfter time.Duration

	// ListenPort is where incoming peer connections are accepted. Zero means
	// an ephemeral port, which is the default and is right for almost
	// everybody: behind a tunnel with no port forwarding nothing can reach us
	// anyway, and anacrolix's own default of a fixed 42069 is both a
	// fingerprint and a second instance on the same host failing to start.
	// Set it when a VPN provider forwards a port.
	ListenPort int

	// hermetic confines the client to loopback: no DHT, so no bootstrap to the
	// public internet and no announce of a fixed test info hash; no UPnP, so no
	// SSDP probe of whatever router the machine is sitting behind; and a
	// loopback listen host rather than a wildcard one, so a test engine is not
	// reachable from the LAN and advertises 127.0.0.1 to its peer instead of
	// 0.0.0.0. It is what makes engine_test.go's opening claim structural
	// rather than aspirational — see start there, which is the only thing that
	// sets it.
	//
	// Unexported because it is not a deployment option and must never become
	// one: a real magnet carries no tracker and no peer list, so the DHT is the
	// only way curator finds anybody at all. On Config rather than in a package
	// var because live_test.go shares this package, and both of its swarm tests
	// — TestLiveDownloadPeakRSS and TestLiveEngineOverTunnel — need exactly the
	// swarm this turns off, so it has to be per-engine rather than per-process.
	//
	// Note what it does not set: DisableTrackers. T56's regression test asserts
	// that a udp4 announcer *was* started (network_test.go), and switching
	// trackers off would make that test pass for the wrong reason. The hermetic
	// magnets carry no trackers, so there is nothing to announce to anyway.
	hermetic bool
}

// clientConfig is everything the torrent client is told before it is built.
//
// It is a function of its own rather than forty lines inside New for one
// reason: anacrolix keeps the ClientConfig on an unexported field and offers no
// accessor, so there is no way to ask a running Client what it was configured
// with. The only way to hold the PRODUCTION configuration to the kill switch is
// for the test to build it the same way New does — and that means one function
// with two callers, not a second copy in a test that agrees today and drifts
// later. See TestTheEngineOpensNoSocketOutsideTheTunnel.
//
// cfg arrives already normalised by New: MaxConns, StallAfter and Log have their
// defaults applied.
func clientConfig(ctx context.Context, cfg Config, dataDir string) *anacrolix.ClientConfig {
	cc := anacrolix.NewDefaultClientConfig()
	cc.DataDir = dataDir
	cc.Seed = true
	cc.ListenPort = cfg.ListenPort
	cc.EstablishedConnsPerTorrent = cfg.MaxConns
	cc.HalfOpenConnsPerTorrent = cfg.MaxConns / 2
	cc.MaxUnverifiedBytes = unverifiedBytes
	// anacrolix logs at its own level through a logger of its own design. Its
	// output is not curator's log format and there is nothing in it a user of
	// this app can act on, so the intent here was to discard it rather than
	// interleave it.
	//
	// It does not do that, and T74 measured it rather than assuming it. This
	// line is INERT. NewDefaultClientConfig leaves Logger at its zero value;
	// WithFilterLevel copies the struct and sets filterLevel without setting
	// nonZero (anacrolix/log logger-core.go:44-47, 55-57); and getLoggers then
	// does `if logger.IsZero() { logger = log.Default }` (client.go:241-243),
	// throwing this away whole. log.Default filters at Warning onto stderr, so
	// anacrolix has been writing warnings and errors straight out of every
	// curator process since phase 6.
	//
	// The comment is corrected rather than the code because the fix is a
	// decision, not a typo: starting from analog.Default makes the filter real,
	// and then Critical honours the sentence above while Error keeps the line
	// that says a torrent has been wedged for good — which is what somebody
	// reporting a stuck download would need to paste. That belongs in a task
	// that argues it, not in a flake fix. See docs/tasks/T74.
	cc.Logger = cc.Logger.FilterLevel(anacrolixQuiet)

	// The two egress paths that are not a socket the client opens for peers, and
	// are therefore not covered by bindConfig's DisableTCP/DisableUTP/NoDHT.
	//
	// They are set for EVERY engine — hermetic, tunnelled and untunnelled alike
	// — because they were the shape of the leak: NoDefaultPortForwarding was set
	// only under cfg.hermetic below, so the test configuration was hardened in a
	// way production never was, and the tests could not have caught it.
	//
	// WebTorrent is the one that carries PAYLOAD outside the tunnel.
	// startWebsocketAnnouncer builds a websocket.Dialer of its own
	// (webtorrent/tracker-client.go:35) and consults none of the dialers
	// bindConfig installs, and WebRTC data channels move pieces. A ws:// or
	// wss:// tracker in a magnet is the whole trigger (torrent.go:2203); 1337x,
	// YTS and TPB hand out udp://, so this has probably never fired. "Probably
	// never" is not "cannot".
	cc.DisableWebtorrent = true

	// UPnP opens sockets on the host and can leave a mapping on the LAN router:
	// client.go:414 runs `go cl.forwardPort()`, which is upnp.Discover, which is
	// SSDP multicast out of every real interface. It is also pointless here —
	// the listen port lives inside the netstack, so a mapping would forward the
	// router's port to something no packet can reach.
	cc.NoDefaultPortForwarding = true

	// DisableWebseeds is deliberately NOT set beside them. Webseeds are plain
	// HTTP through cc.HTTPDialContext (network.go), which is the tunnel, so they
	// are bound rather than unbound and turning them off would cost a real
	// source of bytes for nothing.

	// The kill switch, when there is a network to bind to. It is inside this
	// function rather than beside listenTunnel so that everything the client is
	// told is told in one place and can be read in one place.
	if cfg.Network != nil {
		bindConfig(ctx, cc, cfg.Network, cfg.Log)
	}

	// Outside the Network branch on purpose — bindConfig is the kill switch for
	// a tunnelled engine and never runs for a nil Network, which is what most
	// hermetic tests build — and AFTER it on purpose, which is newer and is the
	// half that bites.
	//
	// hermetic is a confinement, so it has to win over anything bindConfig
	// installs. bindConfig now points DhtStartingNodes at the network's own
	// resolver, and run in the other order a hermetic test that DOES have a
	// Network — TestDownloadThroughANetwork, over loopback — would have had that
	// overwrite the empty list below and bootstrap to router.bittorrent.com
	// through the host after all. The two lines look independent and are not.
	if cfg.hermetic {
		cc.NoDHT = true
		cc.ListenHost = anacrolix.LoopbackListenHost

		// NoDHT alone is not enough, and this is the trap in it. With a Network,
		// attachNetwork builds a DHT server of its own over the shared socket,
		// unconditionally, and that server reads DhtStartingNodes rather than
		// asking whether the DHT is off. Emptying the list is what makes the
		// tunnelled tests local; without it TestDownloadThroughANetwork — itself
		// one of the eight sixty-second awaits — still bootstraps to
		// router.bittorrent.com and still announces the fixed test info hash.
		// An empty list is a no-op rather than a failure: the announcer finds no
		// initial nodes, says so once, and sleeps.
		cc.DhtStartingNodes = func(string) dht.StartingNodesGetter {
			return func() ([]dht.Addr, error) { return nil, nil }
		}
	}

	return cc
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
	if cfg.StallAfter <= 0 {
		cfg.StallAfter = DefaultStallAfter
	}

	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{
		dataDir:    dataDir,
		category:   cfg.Category,
		log:        cfg.Log,
		ctx:        ctx,
		cancel:     cancel,
		stallAfter: cfg.StallAfter,
		now:        time.Now,
		unchecked:  map[string]bool{},
		progress:   map[string]mark{},
		heldConns:  map[string]int{},
		network:    cfg.Network,
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
	}

	client, err := anacrolix.NewClient(clientConfig(ctx, cfg, dataDir))
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
	// — measured, not feared. Dispatch rejects those already, but boot resume
	// re-adds magnets straight out of the database, and a library panic is not
	// something a caller can recover from three layers up.
	parsed, err := metainfo.ParseMagnetUri(magnet)
	if err != nil {
		return fmt.Errorf("engine: %q is not a magnet: %w", clip(magnet), err)
	}
	if parsed.InfoHash.IsZero() {
		return fmt.Errorf("engine: %q carries no info hash", clip(magnet))
	}

	// Already held: adding again is a no-op rather than a reset. Boot resume
	// re-adds every unfinished row, and a second dispatch of the same release
	// converges here exactly as phase 3 promised.
	if _, ok := e.client.Torrent(parsed.InfoHash); ok {
		return nil
	}

	// The metainfo first, when there is one. anacrolix persists the payload and
	// a piece-completion database but never the info dict, so an add by magnet
	// needs a metadata round trip from the swarm — 3.2 s when there are peers,
	// and forever when there are not. With the file, a restart resumes with the
	// network down, which is the entire reason it is written.
	if mi, err := metainfo.LoadFromFile(e.metainfoPath(parsed.InfoHash)); err == nil {
		// Through AddTorrentSpec rather than AddTorrent, so a saved .torrent's
		// announce-list gets the same treatment the magnet's does. It is the
		// other way tracker names reach the client.
		spec := anacrolix.TorrentSpecFromMetaInfo(mi)
		spec.Trackers = resolveTrackers(e.ctx, e.network, spec.Trackers, e.log)
		t, _, err := e.client.AddTorrentSpec(spec)
		if err == nil {
			e.log.Info("resumed from the saved metainfo, without asking the swarm",
				"hash", hashOf(t), "name", t.Name())
			e.start(t)
			return nil
		}
		// Fall through to the magnet: a metainfo that will not load is a reason
		// to ask the swarm, not a reason to fail an add.
		e.log.Warn("the saved metainfo could not be used; falling back to the magnet",
			"hash", torrent.NormalizeHash(parsed.InfoHash.HexString()), "err", err)
	}

	// Not client.AddMagnet: that hands the magnet's tracker URLs straight to
	// anacrolix, whose UDP tracker client resolves each name on the HOST before
	// writing to curator's tunnel socket. See resolveTrackers — it is the one
	// egress path no ClientConfig field can close.
	spec, err := anacrolix.TorrentSpecFromMagnetUri(magnet)
	if err != nil {
		return fmt.Errorf("engine: adding magnet: %w", err)
	}
	spec.Trackers = resolveTrackers(e.ctx, e.network, spec.Trackers, e.log)

	t, _, err := e.client.AddTorrentSpec(spec)
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
	// Marked before this function returns, not inside the goroutine below:
	// AddMagnet's caller can poll the moment it returns, and the completion
	// database will already be answering `complete` for a resumed torrent.
	e.setUnchecked(hashOf(t), true)

	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer e.setUnchecked(hashOf(t), false)
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

		// A payload that is already on disk was not written by this process —
		// this is the first add of this torrent, and the client has just
		// started. Nothing has hashed those bytes: resume is optimistic and
		// reports pieces complete straight out of the completion database
		// without reading one. That is what makes it free, and it means
		// "complete" is a claim about a database until something checks it.
		// Checking costs 15.7–17.8 s for 755 MB, once, against hardlinking a
		// corrupt file into the library and calling it a film.
		//
		// The trigger is the file on disk rather than BytesCompleted, which is
		// read from the completion database asynchronously and is still 0 the
		// instant the info arrives — measured, by a corrupted payload sailing
		// through as complete.
		if path := e.contentPath(t); path != "" {
			if _, err := os.Stat(path); err == nil {
				e.verify(t)
			}
		}
		t.DownloadAll()
	}()
}

// verify re-hashes what is on disk, reporting the torrent as queued while it
// runs — the same word qBittorrent uses for checkingResumeData, and the reason
// the importer leaves it alone until the answer is in.
func (e *Engine) verify(t *anacrolix.Torrent) {
	hash := hashOf(t)

	started := time.Now()
	e.log.Info("verifying data already on disk before trusting it",
		"hash", hash, "name", t.Name(), "bytes", t.BytesCompleted())

	err := t.VerifyDataContext(e.ctx)

	switch {
	case errors.Is(err, context.Canceled):
		return // shutting down; nothing to report
	case err != nil:
		e.log.Warn("verification failed; the torrent will re-fetch whatever it cannot account for",
			"hash", hash, "err", err)
	default:
		e.log.Info("verified", "hash", hash, "took", time.Since(started).Round(time.Millisecond),
			"missing_bytes", t.BytesMissing())
	}
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
		return fmt.Errorf("engine: %w", torrent.WrongCategory{
			Hash: ih.HexString(), Actual: e.category, Required: requireCategory,
		})
	}

	// The path is read before the drop, because a dropped torrent no longer
	// answers questions about itself.
	content := e.contentPath(t)
	t.Drop()

	// Both maps are keyed by hash and would otherwise hold an entry for a
	// torrent that no longer exists, for the lifetime of the process.
	dropped := torrent.NormalizeHash(ih.HexString())
	e.mu.Lock()
	delete(e.progress, dropped)
	delete(e.unchecked, dropped)
	e.mu.Unlock()

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

// Binding reports the address the engine's one socket actually holds, or nil
// when it has none of its own because there was no Network to bind to.
//
// It is READ off the socket rather than remembered from the wiring, and that is
// the entire value of it. The engine does not keep the Network it was given, so
// there is nothing here that could report what it was configured with instead of
// what it got: a caller pairs this with vpn.Tunnel.Owns and gets a fact neither
// package asserted. "curator is bound to the tunnel" stops being a sentence in a
// document and becomes two independent reads that agree.
func (e *Engine) Binding() net.Addr {
	if e.socket == nil {
		return nil
	}
	return e.socket.Addr()
}

// Close stops the client and waits for the goroutines it started. Seeding ends
// with the process, which is the whole seeding policy this phase has.
//
// The order is load-bearing and was arrived at by deadlocking: cancel, then
// wait, then close. Closing the client first parks any goroutine that is inside
// DownloadAll or VerifyData on the client's own mutex, which it never gets, so
// the Wait below never returns. Cancelling first releases everything that is
// waiting on metadata; waiting second lets whatever is mid-call finish against
// a client that is still alive.
func (e *Engine) Close() error {
	e.cancel()
	e.wg.Wait()
	errs := e.client.Close()
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

	// One observation per torrent per tick, feeding both the rate and the stall
	// clock. Taken before the switch so that every branch below — including
	// `completed`, which needs no rate — leaves the sampler in step; a torrent
	// whose observations skipped a state would produce a rate measured across
	// the gap.
	m, stalledNow, firstReport := e.observe(out.Hash, t.BytesCompleted())

	info := t.Info()
	switch {
	case info == nil:
		// No info dict yet: the metadata is still being fetched from the swarm,
		// so nothing about the payload is known — not its size, not its name on
		// disk, not how much of it we have.
		out.State = torrent.StateQueued
	case e.isUnchecked(out.Hash):
		// Added moments ago, or being re-hashed right now. Queued rather than
		// completed, because the completion database answers `complete` the
		// instant a torrent is added over existing data — and between the add
		// and the verify there is a window in which the poller would otherwise
		// see `completed` and hardlink a file nothing has vouched for. Measured
		// by a corrupted payload sailing straight through it.
		out.State = torrent.StateQueued
	case t.Complete().Bool():
		out.State = torrent.StateCompleted
		out.Progress = 1
	default:
		out.State = torrent.StateDownloading
		if length := t.Length(); length > 0 {
			out.Progress = float64(t.BytesCompleted()) / float64(length)
		}
		out.DownloadRate = e.rateOf(m)
	}

	// Known as soon as the info dict is, which is what lets the screen say
	// "3.2 GB of 8.1 GB" the moment there is an 8.1 GB to name. Before that it
	// stays 0 and the screen says nothing rather than "of 0 B".
	if info != nil {
		out.SizeBytes = t.Length()
	}

	// StateFailed is deliberately unreachable. This engine has no equivalent of
	// qBittorrent's `error` or `missingFiles`: a torrent it cannot make
	// progress on is not failed, it is stalled — nobody is seeding it — and
	// saying `failed` would tell somebody their download died when what
	// happened is that they picked an unpopular release.
	if out.State == torrent.StateQueued || out.State == torrent.StateDownloading {
		if stalled, reason := e.stalled(t, out, m, stalledNow, firstReport); stalled {
			out.State = torrent.StateStalled
			out.Reason = reason
			// A stalled torrent is not moving by definition, so whatever the
			// staleness window still allows is a number about the past.
			out.DownloadRate = 0
		}
	}

	// The hold wins over any stall diagnosis, and it has to be last. A held
	// torrent gains no bytes, so e.stalled is perfectly correct that it is
	// stalled and perfectly wrong about why — it would report "nobody appears to
	// be seeding this release" about a download curator switched off itself.
	if reason := e.holdReason(); reason != "" {
		out = heldState(out, reason)
	}
	return out
}

func (e *Engine) isUnchecked(hash string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.unchecked[hash]
}

func (e *Engine) setUnchecked(hash string, yes bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if yes {
		e.unchecked[hash] = true
		return
	}
	delete(e.unchecked, hash)
}

// stalled reports whether this torrent has gained nothing for long enough to
// say so out loud, with the reason when it has, and logs it once when it first
// does.
//
// The trigger is byte progress, not peer count, because "no peers" and "fifty
// peers, none of whom will send" are the same experience — a progress bar that
// does not move. The peer count goes in the reason, which is the part a human
// can act on: nobody is seeding this release, so pick another one.
//
// The reason is computed on EVERY stalled tick and the log line is still
// written once. Those used to be the same event, when the sentence only ever
// went to the log; since T55 it also goes into the row, and a row is rewritten
// every tick — so a reason produced only on the first tick would be missing
// from every write after a restart, when `reported` is back to false but the
// stall is not. `t.Stats()` is a locked read of counters the client already
// keeps, so paying for it per stalled torrent per tick costs nothing worth
// arranging around.
//
// A verifying torrent is exempt: it is busy, just not with the network. So is a
// completed one, which by definition never gains another byte.
// observe records one torrent's byte count and returns what that means.
//
// **It is the ONE sampler**, and that is deliberate: the rate and the stall
// clock are the same measurement read two ways, so computing them separately
// would be two maps, two lock acquisitions per torrent per tick, and two
// answers that can disagree about whether anything is arriving. `describe`
// calls this once and hands the result to `stalled`.
//
// The rate's numerator is `t.BytesCompleted()` — the same number `Progress` is
// built from — rather than `t.Stats().BytesReadUsefulData`, so the rate and the
// bar can never tell different stories. `t.Stats()` stays what it was: the peer
// count behind the stall sentence.
func (e *Engine) observe(hash string, bytes int64) (m mark, stalled, firstReport bool) {
	now := e.now()

	e.mu.Lock()
	defer e.mu.Unlock()

	previous, seen := e.progress[hash]
	if !seen {
		fresh := mark{bytes: bytes, since: now}
		e.progress[hash] = fresh
		return fresh, false, false
	}

	if previous.bytes != bytes {
		// It moved. Carry the old rate unless enough time has passed to measure
		// a new one honestly — see rateFloor.
		fresh := mark{bytes: bytes, since: now, rate: previous.rate}
		if elapsed := now.Sub(previous.since); elapsed >= rateFloor {
			// max(0): a re-hash or a dropped piece can move the count DOWN, and
			// a negative download rate is not a thing to put on a screen.
			perSecond := float64(bytes-previous.bytes) / elapsed.Seconds()
			if perSecond < 0 {
				perSecond = 0
			}
			fresh.rate = int64(perSecond)
		}
		e.progress[hash] = fresh
		return fresh, false, false
	}

	// It did not move. The stall clock runs from `since`, which is untouched
	// here on purpose — that is the whole reason it means "last moved".
	stalled = now.Sub(previous.since) >= e.stallAfter
	firstReport = stalled && !previous.reported
	if firstReport {
		previous.reported = true
		e.progress[hash] = previous
	}
	return previous, stalled, firstReport
}

// rateOf is what observe's mark is worth reporting as, now.
func (e *Engine) rateOf(m mark) int64 {
	if e.now().Sub(m.since) >= rateStaleAfter {
		return 0
	}
	return m.rate
}

func (e *Engine) stalled(t *anacrolix.Torrent, out torrent.Torrent, m mark, stalled, firstTime bool) (bool, string) {
	if !stalled {
		return false, ""
	}
	now := e.now()
	previous := m

	// Said by the backend rather than by the poller, because the reason is a
	// fact only the backend has: the poller sees a percentage that did not move
	// and cannot tell "nobody has this" from "nobody will send it".
	stats := t.Stats()
	reason := stallReason(stats.ActivePeers, t.Info() == nil)

	if firstTime {
		// Once per torrent, not once per tick — a five-second poll must not
		// produce a five-second warning.
		e.log.Warn("torrent is stalled: nothing has arrived for a while",
			"hash", out.Hash, "name", out.Name, "for", now.Sub(previous.since).Round(time.Second),
			"progress", fmt.Sprintf("%.1f%%", out.Progress*100),
			"peers", stats.ActivePeers, "seeders", stats.ConnectedSeeders,
			"reason", reason)
	}
	return true, reason
}

// stallReason is the sentence somebody reads — in the Logs screen since T36,
// and since T55 under the badge in Activity as well. It is prose in both
// places, and deliberately not a code with a table of renderings beside it:
// the screen prints what the backend said.
func stallReason(peers int, noMetadata bool) string {
	switch {
	case peers == 0 && noMetadata:
		return "no peers have answered, so not even the metadata has arrived — nobody appears to be seeding this release"
	case peers == 0:
		return "no peers are connected — nobody appears to be seeding this release"
	case noMetadata:
		return "peers are connected but none of them has sent the metadata"
	default:
		return "peers are connected but none of them is sending data"
	}
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
// check library.AssertInside makes, for the same reason: the last component
// came from a torrent's own metadata.
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
