package qbit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/DulsaraNethmin/curator/internal/torrent"
)

// The stop/start endpoints, and there are four names for two operations.
//
// **qBittorrent 5.0 renamed pause to stop**, and the Web API followed:
// `torrents/pause` became `torrents/stop` and `torrents/resume` became
// `torrents/start`. state.go has documented the matching rename of the STATE
// names — `pausedDL` → `stoppedDL` — since phase 3, so this is the same change
// arriving on the other half of the API.
//
// Both spellings are sent, newest first, because curator does not know which
// qBittorrent it is pointed at: the Pi's was 5.1.2, and a stranger's `docker
// run` could be a 4.x that has never heard of `torrents/stop`. A 404 on the
// first is not a failure, it is the answer to "which vocabulary does this
// server speak" — see stopStart.
const (
	pathTorrentsStop   = "torrents/stop"   // 5.x
	pathTorrentsPause  = "torrents/pause"  // 4.x, and an alias in early 5.x
	pathTorrentsStart  = "torrents/start"  // 5.x
	pathTorrentsResume = "torrents/resume" // 4.x, and an alias in early 5.x
)

// PauseTorrent stops one torrent, refusing one that is not in the required
// category.
//
// The package comment used to say this could not exist — *"It deliberately
// cannot pause, resume or reprioritise anything: the \*arr stack shares this
// qBittorrent until phase 6"*. That reason expired on 2026-08-18, when T54
// removed the \*arr stack from the Pi. The guard it justified is kept anyway
// and is now the only thing standing between curator and somebody else's
// torrents: the category is checked before anything is sent, exactly as
// DeleteTorrent checks it (docs/decisions.md D55).
func (c *Client) PauseTorrent(ctx context.Context, hash, requireCategory string) error {
	return c.stopStart(ctx, hash, requireCategory, "pause", pathTorrentsStop, pathTorrentsPause)
}

// ResumeTorrent starts one torrent again, with the same category guard.
func (c *Client) ResumeTorrent(ctx context.Context, hash, requireCategory string) error {
	return c.stopStart(ctx, hash, requireCategory, "resume", pathTorrentsStart, pathTorrentsResume)
}

// stopStart is the shared body: find it, check whose it is, then send.
//
// It looks the torrent up first for the reason DeleteTorrent does — the
// category is not a parameter of the stop call, so the only way to honour it is
// to read the torrent's own category and refuse before sending anything.
func (c *Client) stopStart(ctx context.Context, hash, requireCategory, verb, modern, legacy string) error {
	wire := wireHash(hash)
	if wire == "" {
		return fmt.Errorf("qbit %s: %s a torrent by an empty hash", modern, verb)
	}

	existing, err := c.TorrentByHash(ctx, wire)
	if err != nil {
		return err
	}
	if existing == nil {
		// NOT DeleteTorrent's "already gone is success". A delete that has
		// already happened is a delete nobody could retry; stopping a torrent
		// qBittorrent does not have is a request about nothing, and answering
		// 200 would tell the screen a row is paused that has no torrent.
		return fmt.Errorf("qbit %s: %s %s: %w", modern, verb, wire, torrent.ErrNotFound)
	}
	if requireCategory != "" && !strings.EqualFold(existing.Category, requireCategory) {
		return fmt.Errorf("qbit %s: %w", modern, torrent.WrongCategory{
			Hash: wire, Actual: existing.Category, Required: requireCategory,
		})
	}

	form := url.Values{}
	form.Set("hashes", wire)

	if _, err := c.do(ctx, http.MethodPost, modern, nil, form); err != nil {
		if !notFound(err) {
			return err
		}
		// This qBittorrent predates the rename. Try the old name once — and if
		// that 404s too, report THAT error rather than this one, because the
		// old spelling is the one a 4.x server should have known.
		if _, err := c.do(ctx, http.MethodPost, legacy, nil, form); err != nil {
			return err
		}
	}
	return nil
}

// notFound reports whether a failure was qBittorrent answering 404.
//
// It reads the message rather than a typed error because `do` renders every
// non-200 the same way, and giving that one call site a typed error would mean
// a second error shape for four callers that do not want one. The string it
// looks for is `do`'s own format, and the test that pins the fallback would
// fail if either changed.
func notFound(err error) bool {
	if err == nil || errors.Is(err, ErrAuth) {
		return false
	}
	return strings.Contains(err.Error(), fmt.Sprintf("answered %d ", http.StatusNotFound))
}
