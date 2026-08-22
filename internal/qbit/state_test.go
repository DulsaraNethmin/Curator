package qbit

import (
	"testing"

	"github.com/DulsaraNethmin/curator/internal/torrent"
)

// TestMapState walks every row of the table in docs/phase-3.md, plus the two
// spellings qBittorrent 5.x uses for the paused states, plus a state nobody has
// ever shipped.
//
// The last one is the point of the test: an unrecognised state must map to
// downloading. Mapping it to failed would tell a human their download died
// every time qBittorrent gained a state, and invite them to grab a second copy
// of a file that was fine.
func TestMapState(t *testing.T) {
	cases := []struct {
		qbit string
		want string
	}{
		// queued
		{"queuedDL", torrent.StateQueued},
		{"metaDL", torrent.StateQueued},
		{"allocating", torrent.StateQueued},
		{"checkingDL", torrent.StateQueued},
		{"checkingResumeData", torrent.StateQueued},

		// paused — and NOT queued, which they were until T107. `queued` promises
		// a torrent about to start; these mean one that will not until somebody
		// says so, and the Activity row now has a Resume button to draw on the
		// difference.
		{"pausedDL", torrent.StatePaused},
		{"stoppedDL", torrent.StatePaused}, // what 5.1.2 sends instead of pausedDL

		// downloading
		// stalled
		{"stalledDL", torrent.StateStalled},

		// downloading
		{"downloading", torrent.StateDownloading},
		{"forcedDL", torrent.StateDownloading},
		// `moving` deserves its own note: qBittorrent reports it while relocating
		// a finished torrent, and mapping it to downloading is what keeps the
		// importer away from a content_path that is mid-move — the one thing the
		// unmeasured complete-vs-incomplete question could have bitten us on.
		{"moving", torrent.StateDownloading},

		// completed
		{"uploading", torrent.StateCompleted},
		{"stalledUP", torrent.StateCompleted},
		{"queuedUP", torrent.StateCompleted},
		{"forcedUP", torrent.StateCompleted},
		{"checkingUP", torrent.StateCompleted},
		{"pausedUP", torrent.StateCompleted},
		{"stoppedUP", torrent.StateCompleted}, // what 5.1.2 sends instead of pausedUP

		// failed — the only two states that mean the file is not coming
		{"error", torrent.StateFailed},
		{"missingFiles", torrent.StateFailed},

		// unrecognised
		{"unknown", torrent.StateDownloading},   // qBittorrent's own literal "unknown"
		{"quantumDL", torrent.StateDownloading}, // invented; stands in for a future release
		{"", torrent.StateDownloading},          // a field that never arrived
		{"  downloading  ", torrent.StateDownloading},
	}

	for _, tc := range cases {
		if got := mapState(tc.qbit); got != tc.want {
			t.Errorf("mapState(%q) = %q, want %q", tc.qbit, got, tc.want)
		}
	}
}

// TestMapStateNeverInventsFailed guards the asymmetry directly: only the two
// states that genuinely mean "this file is not coming" may produce failed.
// A future edit that maps a paused or stalled state to failed fails here, not in
// production three weeks later.
func TestMapStateNeverInventsFailed(t *testing.T) {
	failed := map[string]bool{"error": true, "missingFiles": true}
	for state, mapped := range qbitStates {
		if mapped == torrent.StateFailed && !failed[state] {
			t.Errorf("qbitStates[%q] = %q; only %v may map to failed", state, mapped, failed)
		}
	}
}

// T55: stalled is the one state this backend can say anything about, and it is
// the only one that may carry a sentence. `stalledUP` in particular must not —
// a finished torrent with no leechers is not a problem, it is a Tuesday, and a
// reason on a completed row would render under a badge that is doing fine.
func TestStalledReasonIsOnlyForStalled(t *testing.T) {
	if got := stalledReason(torrent.StateStalled); got == "" {
		t.Error("a stalled torrent has no sentence; the badge would be alone on screen again")
	}
	for _, state := range []string{
		torrent.StateQueued, torrent.StateDownloading, torrent.StateCompleted, torrent.StateFailed,
	} {
		if got := stalledReason(state); got != "" {
			t.Errorf("stalledReason(%q) = %q, want empty — only stalled has anything to explain", state, got)
		}
	}

	// Reached through the mapping the wire actually goes through, so the two
	// spellings cannot drift apart: whatever qBittorrent calls it, the sentence
	// appears exactly when curator answered `stalled`.
	if got := stalledReason(mapState("stalledDL")); got == "" {
		t.Error("stalledDL produced no reason")
	}
	if got := stalledReason(mapState("stalledUP")); got != "" {
		t.Errorf("stalledUP produced %q; it maps to completed and completed has no reason", got)
	}
}
