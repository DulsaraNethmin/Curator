package torrent

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/DulsaraNethmin/curator/internal/store"
)

// TestStatesMatchTheColumn is the whole reason these constants were moved
// rather than copied. There are two sets of the same strings — one facing
// the backends, one facing the downloads.state column — and they are only safe
// while they are identical. A third set would have been how they drift.
func TestStatesMatchTheColumn(t *testing.T) {
	for _, c := range []struct{ here, column string }{
		{StateQueued, store.DownloadQueued},
		{StateDownloading, store.DownloadDownloading},
		{StateCompleted, store.DownloadCompleted},
		{StateFailed, store.DownloadFailed},
		{StateStalled, store.DownloadStalled},
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

// WrongCategory must answer errors.Is(err, ErrWrongCategory) through any amount
// of wrapping, because that is the only thing standing between a refused delete
// and a 500 (internal/api/movies_delete.go). It is a silent failure otherwise:
// the type still compiles, the message still reads, and the status changes.
func TestWrongCategoryIsStillTheSentinel(t *testing.T) {
	err := error(WrongCategory{Hash: "ABC", Actual: "radarr", Required: "curator"})
	if !errors.Is(err, ErrWrongCategory) {
		t.Fatal("errors.Is lost the sentinel, and the API answers 500 instead of 409")
	}
	if !errors.Is(fmt.Errorf("qbit torrents/delete: %w", err), ErrWrongCategory) {
		t.Error("errors.Is lost the sentinel through a backend's own wrap")
	}

	// The sentinel's text is reached through Unwrap and must not be restated in
	// the message — the two together is what read as one fact said twice.
	if got := err.Error(); strings.Contains(got, ErrWrongCategory.Error()) {
		t.Errorf("Error() restates the sentinel: %q", got)
	}

	// Both names are carried, because which app owns the torrent is the only
	// actionable word in the refusal the API writes from this.
	var wrong WrongCategory
	if !errors.As(fmt.Errorf("engine: %w", err), &wrong) {
		t.Fatal("errors.As cannot recover the categories through a wrap")
	}
	if wrong.Actual != "radarr" || wrong.Required != "curator" {
		t.Errorf("recovered %+v", wrong)
	}
}
