package qbit

import "testing"

// TestMapState walks every row of the table in docs/phase-3.md, plus the two
// spellings qBittorrent 5.x uses for the paused states, plus a state nobody has
// ever shipped.
//
// The last one is the point of the test: an unrecognised state must be
// StateDownloading. Mapping it to StateFailed would tell a human their download
// died every time qBittorrent gained a state, and invite them to grab a second
// copy of a file that was fine.
func TestMapState(t *testing.T) {
	cases := []struct {
		qbit string
		want string
	}{
		// queued
		{"queuedDL", StateQueued},
		{"stalledDL", StateQueued},
		{"metaDL", StateQueued},
		{"allocating", StateQueued},
		{"checkingDL", StateQueued},
		{"checkingResumeData", StateQueued},
		{"pausedDL", StateQueued},
		{"stoppedDL", StateQueued}, // what 5.1.2 sends instead of pausedDL

		// downloading
		{"downloading", StateDownloading},
		{"forcedDL", StateDownloading},
		{"moving", StateDownloading},

		// completed
		{"uploading", StateCompleted},
		{"stalledUP", StateCompleted},
		{"queuedUP", StateCompleted},
		{"forcedUP", StateCompleted},
		{"checkingUP", StateCompleted},
		{"pausedUP", StateCompleted},
		{"stoppedUP", StateCompleted}, // what 5.1.2 sends instead of pausedUP

		// failed — the only two states that mean the file is not coming
		{"error", StateFailed},
		{"missingFiles", StateFailed},

		// unrecognised
		{"unknown", StateDownloading},   // qBittorrent's own literal "unknown"
		{"quantumDL", StateDownloading}, // invented; stands in for a future release
		{"", StateDownloading},          // a field that never arrived
		{"  downloading  ", StateDownloading},
	}

	for _, tc := range cases {
		if got := MapState(tc.qbit); got != tc.want {
			t.Errorf("MapState(%q) = %q, want %q", tc.qbit, got, tc.want)
		}
	}
}

// TestMapStateNeverInventsFailed guards the asymmetry directly: only the two
// states that genuinely mean "this file is not coming" may produce StateFailed.
// A future edit that maps a paused or stalled state to failed fails here, not in
// production three weeks later.
func TestMapStateNeverInventsFailed(t *testing.T) {
	failed := map[string]bool{"error": true, "missingFiles": true}
	for state, mapped := range qbitStates {
		if mapped == StateFailed && !failed[state] {
			t.Errorf("qbitStates[%q] = %q; only %v may map to failed", state, mapped, failed)
		}
	}
}
