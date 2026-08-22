// Package torrent is the torrent as curator sees it: the six facts every
// backend can answer, and the states the downloads table carries.
//
// It exists so that internal/download, internal/importer and internal/api are
// written once and work against either backend — internal/qbit, which asks a
// qBittorrent over HTTP, and the embedded engine, which downloads it in this
// process. A backend's own vocabulary is that backend's business and is
// translated on the way out: qBittorrent's 23 states, its lower-case hashes and
// its second filesystem namespace never cross this boundary.
//
// The type carries seven fields because seven are read. qBittorrent decodes
// `size` and `save_path` as well and nothing has ever looked at them, and a
// field nobody reads is a field that can be wrong for a year without anyone
// noticing.
//
// The seventh is Reason, and T36 argued against it while there were six: the
// reason a torrent is stalled is a fact only a backend has, and it went to the
// log because carrying it any further needed a column. T55 added the column, so
// the sentence now travels on the value that already crosses this boundary
// rather than being re-derived by anyone downstream.
//
// This package is a leaf — stdlib only — so every other package can depend on
// it and none of them pays for the dependency.
package torrent

import (
	"errors"
	"fmt"
	"strings"
)

// The states a torrent can be in, as untyped string constants so they assign
// straight into store.Download without a conversion.
//
// These are the same strings store.Download* carries, and they are the whole
// vocabulary a backend may produce: `imported` is the importer's to set and no
// backend can ever produce it, because no torrent client knows what a library
// is.
const (
	StateQueued      = "queued"
	StateDownloading = "downloading"
	StateCompleted   = "completed"
	StateFailed      = "failed"

	// StateStalled is a torrent that is added, wanted, and getting nowhere:
	// nobody is seeding it, or the peers that are will not send. It is a
	// description rather than a failure — the torrent stays added and a peer
	// appearing puts it straight back to downloading — and it exists because
	// "queued" and "a magnet nobody has" looked identical for two phases.
	StateStalled = "stalled"

	// StatePaused is a torrent somebody stopped on purpose.
	//
	// **It is a state and not a Reason, and that is the whole of D55.** The hold
	// the VPN sentinel applies reuses StateStalled with a sentence, and that is
	// right for a hold: it is process-wide, has no per-row control, and its
	// reason is the only actionable thing on screen. A user pause is the
	// opposite on all three counts, and the decisive one is that **the row has
	// to draw a Resume button** — testing `reason.includes("paused")` in the UI
	// is exactly the translation table Reason's own doc forbids.
	//
	// It also stops the engine's stall detector lying: a paused torrent gains no
	// bytes, so after DefaultStallAfter it would report "no peers are connected"
	// about a download curator switched off on request.
	StatePaused = "paused"
)

// ErrWrongCategory reports that a torrent exists but does not belong to the
// category the caller required, so it was NOT deleted.
//
// It lives here rather than in a backend because internal/api tests for it with
// errors.Is to answer 409, and an API layer reaching into one particular client
// for an error value is exactly the coupling this package removes. Both backends
// wrap this one.
var ErrWrongCategory = errors.New("the torrent is not in the required category")

// ErrNotFound reports that the backend has no torrent with that hash.
//
// It lives here for the same reason ErrWrongCategory does: internal/api tests
// for it with errors.Is to answer 404, and reaching into one particular backend
// for an error value is the coupling this package exists to remove. It is
// distinct from "already gone is success" — DeleteTorrent keeps that behaviour,
// because a delete that has already happened is a delete nobody can retry,
// while pausing a torrent that is not there is a request about nothing.
var ErrNotFound = errors.New("no torrent with that hash")

// WrongCategory is ErrWrongCategory carrying the two category names, because
// which app owns the torrent is the only actionable word in that refusal and a
// handler has no other way to reach it.
//
// Both backends construct this one, so the sentence stops differing by backend:
// qbit wrapped the sentinel with "qbit torrents/delete: " and the engine with
// "engine: ", and a user was shown whichever happened to be running.
//
// Error() does NOT restate ErrWrongCategory. The sentinel is reached through
// Unwrap, so errors.Is keeps answering while its text stays out of the message —
// the shape jellyfin's `unreachable` uses for the same reason.
type WrongCategory struct {
	// Hash is whatever form the backend had. Callers must not render it beside
	// another copy: the store holds upper-case and qBittorrent's wire form is
	// lower-case, and the two together read as two different torrents.
	Hash     string
	Actual   string
	Required string
}

func (e WrongCategory) Error() string {
	return fmt.Sprintf("%s is in category %q, not %q", e.Hash, e.Actual, e.Required)
}

func (e WrongCategory) Unwrap() error { return ErrWrongCategory }

// Torrent is one torrent, however it is being downloaded.
type Torrent struct {
	// Hash is the upper-case 40-hex info hash — the form indexer.InfoHash
	// produces and the downloads table stores. qBittorrent reports lower-case
	// and its `hashes=` filter demands it; that is one backend's wire format,
	// normalised before it reaches here, and comparing the two raw finds
	// nothing for ever with no error to explain why.
	Hash string

	// Name is what to call it on screen. For a magnet it is the `dn` until the
	// metadata arrives and the real torrent name replaces it — true of both
	// backends, because it is a property of magnets.
	Name string

	// State is one of those above: curator's vocabulary, never a backend's.
	// A backend that has states of its own maps them before this is filled in.
	State string

	// Progress is 0..1, not a percentage. The poller writes it into the row as
	// it stands and the UI multiplies.
	Progress float64

	// ContentPath is where the payload is: the file for a single-file torrent,
	// the folder for a multi-file one, which is what library.FindFeature
	// expects either way.
	//
	// It is reported in the backend's own namespace. qBittorrent's mount on the
	// Pi is host /media/storage/media/downloads → container /downloads, so the
	// path it names need not exist on the machine curator runs on, and the
	// importer translates (docs/decisions.md D13).
	ContentPath string

	// Category is what scopes a torrent to curator: everything it adds goes in
	// `curator`, and the *arr stack's torrents are in `radarr` and `sonarr`.
	// For a backend curator owns outright it is a constant rather than a
	// filter, and that is a strengthening — see docs/decisions.md D22.
	Category string

	// Reason is why this torrent is getting nowhere, in prose, or empty.
	//
	// Empty means two things that need no distinguishing: this torrent is fine,
	// or this backend cannot say. It is written by the backend because only the
	// backend knows — the poller sees a percentage that did not move and cannot
	// tell "nobody has this" from "nobody will send it", which is precisely the
	// distinction worth showing.
	//
	// It is prose and not a code, because it is rendered verbatim: a code would
	// need a translation table in the UI, which is a second vocabulary for the
	// screen to drift out of step with. It accompanies StateStalled and nothing
	// else, and it is written in the same tick as the state it explains — a
	// reason that outlives its state is worse than none.
	Reason string

	// DownloadRate is bytes per second right now, or 0 when this backend cannot
	// say — which is not the same as "nothing is arriving", and the screen draws
	// nothing rather than "0 B/s" for it.
	//
	// **Never persisted.** A rate is true for one instant, and download.Poller
	// only writes a row when something moved (poller.go) — a rate column would
	// change on every tick and defeat that condition permanently, turning a
	// five-second poll into a five-second write. It is joined onto the rows at
	// read time instead (docs/decisions.md D56).
	DownloadRate int64

	// SizeBytes is the payload's total length, or 0 before the metadata has
	// arrived.
	//
	// There is deliberately no BytesCompleted beside it: Progress is the
	// fraction of this that is here, so `SizeBytes * Progress` is the number,
	// and a second field carrying it a second way is a second thing that can
	// disagree with the bar.
	SizeBytes int64
}

// NormalizeHash puts an info hash in the case curator uses: upper, matching
// indexer.InfoHash and the downloads table.
//
// It is the one exported normaliser. A backend whose wire format is the other
// case keeps that conversion to itself, where it belongs, so there is no longer
// a pair of functions with the same name doing opposite things.
func NormalizeHash(hash string) string {
	return strings.ToUpper(strings.TrimSpace(hash))
}
