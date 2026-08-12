package qbit

import "strings"

// The states the downloads table carries, as untyped string constants so they
// assign straight into store.Download without a conversion. `imported` is phase
// 4's and is deliberately absent here: qBittorrent knows nothing about importing,
// so nothing in this package can ever produce it.
const (
	StateQueued      = "queued"
	StateDownloading = "downloading"
	StateCompleted   = "completed"
	StateFailed      = "failed"
)

// qbitStates is the mapping in docs/phase-3.md, spelled exactly as qBittorrent's
// `state` field arrives.
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
	"queuedDL":           StateQueued,
	"stalledDL":          StateQueued,
	"metaDL":             StateQueued,
	"allocating":         StateQueued,
	"checkingDL":         StateQueued,
	"checkingResumeData": StateQueued,

	// A partial download that has been paused is still partial: the file we want
	// is not there, so it is queued rather than failed.
	"pausedDL":  StateQueued,
	"stoppedDL": StateQueued,

	"downloading": StateDownloading,
	"forcedDL":    StateDownloading,
	"moving":      StateDownloading,

	// Everything past 100%. `pausedUP` belongs here and not in some "paused"
	// limbo: a torrent that finished downloading and was then paused has the file
	// we wanted, which is the only question this state answers.
	"uploading":  StateCompleted,
	"stalledUP":  StateCompleted,
	"queuedUP":   StateCompleted,
	"forcedUP":   StateCompleted,
	"checkingUP": StateCompleted,
	"pausedUP":   StateCompleted,
	"stoppedUP":  StateCompleted,

	"error":        StateFailed,
	"missingFiles": StateFailed,
}

// MapState translates one qBittorrent state into the downloads table's.
//
// Anything unrecognised — including qBittorrent's own literal "unknown" and an
// empty string — becomes StateDownloading, never StateFailed. A state we have not
// seen is far more likely to be a newer qBittorrent than a broken torrent, and
// the cost of the two mistakes is not symmetric: guessing `downloading` means one
// more poll tick, while guessing `failed` tells a human their download died and
// invites them to grab a second copy of a file that is fine.
func MapState(qbitState string) string {
	if state, ok := qbitStates[strings.TrimSpace(qbitState)]; ok {
		return state
	}
	return StateDownloading
}
