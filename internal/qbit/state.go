package qbit

import (
	"strings"

	"github.com/DulsaraNethmin/curator/internal/torrent"
)

// qbitStates is the mapping in docs/phase-3.md, spelled exactly as qBittorrent's
// `state` field arrives. The right-hand side is curator's vocabulary, which
// lives in internal/torrent because two backends now answer in it — these
// constants used to be declared here, and a second copy of a vocabulary is how
// a vocabulary drifts.
//
// Two entries are not in that table. qBittorrent 5.0 renamed pause/resume to
// stop/start and its Web API states followed — `pausedDL` became `stoppedDL` and
// `pausedUP` became `stoppedUP` — so on the Pi's 5.1.2 the stopped* spellings are
// the ones we will actually see. Without them a finished-and-stopped torrent
// would fall through to the default and report `downloading` forever, and phase 4
// would never import it. The paused* spellings stay because docs/phase-3.md lists
// them and a 4.x instance still sends them; both cost one map entry.
var qbitStates = map[string]string{
	// Not started yet, or working on something that is not the payload.
	"queuedDL":           torrent.StateQueued,
	"metaDL":             torrent.StateQueued,
	"allocating":         torrent.StateQueued,
	"checkingDL":         torrent.StateQueued,
	"checkingResumeData": torrent.StateQueued,

	// A partial download that has been stopped is still partial — the file we
	// want is not there — but it is not QUEUED either, and it read that way for
	// four phases. `queued` promises a torrent that is about to start; these two
	// mean one that will not until somebody says so.
	//
	// They became distinguishable when there was somewhere to put them: T107
	// added StatePaused because the row now draws a Resume button, and a button
	// cannot be drawn on a state that also means "starting shortly". `pausedUP`
	// and `stoppedUP` below are NOT this — a torrent that finished and was then
	// stopped has the file, so it stays completed and the importer still runs.
	"pausedDL":  torrent.StatePaused,
	"stoppedDL": torrent.StatePaused,

	// stalledDL is qBittorrent's word for exactly what StateStalled describes:
	// wanted, added, and nobody is sending. It used to map to queued, which is
	// where the "sitting at 0% for ever with no explanation" confusion came
	// from on this backend too. stalledUP stays completed — a finished torrent
	// with no leechers is not a problem, it is a Tuesday.
	"stalledDL": torrent.StateStalled,

	"downloading": torrent.StateDownloading,
	"forcedDL":    torrent.StateDownloading,
	"moving":      torrent.StateDownloading,

	// Everything past 100%. `pausedUP` belongs here and not in some "paused"
	// limbo: a torrent that finished downloading and was then paused has the file
	// we wanted, which is the only question this state answers.
	"uploading":  torrent.StateCompleted,
	"stalledUP":  torrent.StateCompleted,
	"queuedUP":   torrent.StateCompleted,
	"forcedUP":   torrent.StateCompleted,
	"checkingUP": torrent.StateCompleted,
	"pausedUP":   torrent.StateCompleted,
	"stoppedUP":  torrent.StateCompleted,

	"error":        torrent.StateFailed,
	"missingFiles": torrent.StateFailed,
}

// mapState translates one qBittorrent state into curator's.
//
// It is unexported: it runs on the way out of this package, in info.neutral,
// and nothing outside has any business knowing that `stalledUP` is a word.
//
// Anything unrecognised — including qBittorrent's own literal "unknown" and an
// empty string — becomes StateDownloading, never StateFailed. A state we have not
// seen is far more likely to be a newer qBittorrent than a broken torrent, and
// the cost of the two mistakes is not symmetric: guessing `downloading` means one
// more poll tick, while guessing `failed` tells a human their download died and
// invites them to grab a second copy of a file that is fine.
func mapState(qbitState string) string {
	if state, ok := qbitStates[strings.TrimSpace(qbitState)]; ok {
		return state
	}
	return torrent.StateDownloading
}

// stalledReason is what this backend can honestly say about a stalled torrent,
// and it is one sentence because one sentence is all `stalledDL` supports.
//
// qBittorrent's own definition of the state is "being downloaded, but no
// connections were made", so saying that is translation rather than invention.
// What it cannot say is the rest: GET /torrents/info carries no peer count, so
// the embedded engine's distinction between nobody answering, peers that have
// not sent the metadata and peers that will not send data is unavailable here.
// Composing a richer sentence out of a state string would be this package
// claiming to know something it read nowhere.
//
// Every other state returns "": a torrent that is fine has no reason, and a
// reason that outlives the state it explains is worse than none.
func stalledReason(state string) string {
	if state != torrent.StateStalled {
		return ""
	}
	return "no connections have been made — nobody appears to be seeding this release"
}
