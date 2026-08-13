package torrent

import (
	"testing"

	"github.com/DulsaraNethmin/curator/internal/store"
)

// TestStatesMatchTheColumn is the whole reason these constants were moved
// rather than copied. There are two sets of the same four strings — one facing
// the backends, one facing the downloads.state column — and they are only safe
// while they are identical. A third set would have been how they drift.
func TestStatesMatchTheColumn(t *testing.T) {
	for _, c := range []struct{ here, column string }{
		{StateQueued, store.DownloadQueued},
		{StateDownloading, store.DownloadDownloading},
		{StateCompleted, store.DownloadCompleted},
		{StateFailed, store.DownloadFailed},
	} {
		if c.here != c.column {
			t.Errorf("state %q does not match the column's %q", c.here, c.column)
		}
	}
}

// TestNormalizeHash: upper-case, trimmed, idempotent. The downloads table is
// keyed on this form, so a hash that skips it is a lookup that silently finds
// nothing — which looks exactly like a torrent the client never accepted.
func TestNormalizeHash(t *testing.T) {
	const upper = "481B6E3617BE4C88F96CB25E47C9D8272130071E"

	for _, c := range []struct{ name, in, want string }{
		{"lower-case, as qBittorrent reports it", "481b6e3617be4c88f96cb25e47c9d8272130071e", upper},
		{"already upper", upper, upper},
		{"padded", "  " + upper + "\n", upper},
		{"empty stays empty", "   ", ""},
	} {
		if got := NormalizeHash(c.in); got != c.want {
			t.Errorf("%s: NormalizeHash(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
