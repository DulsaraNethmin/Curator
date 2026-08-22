package engine

import (
	"testing"
	"time"
)

// sampler is the smallest Engine `observe` needs: a clock, the map, and the
// stall threshold. Built by hand rather than through start() because the rate
// arithmetic is about byte counts and elapsed time, and a real swarm would make
// both of those something to wait for rather than something to assert.
func sampler(t *testing.T, at *time.Time, stallAfter time.Duration) *Engine {
	t.Helper()
	return &Engine{
		progress:   map[string]mark{},
		stallAfter: stallAfter,
		now:        func() time.Time { return *at },
	}
}

// A rate is a measured delta over a measured interval, and the interval is the
// half that is easy to get wrong: anacrolix reports cumulative counters only, so
// there is no rate to read anywhere and assuming the poll interval would be a
// number that is right only while nothing else calls Torrents.
func TestTheRateIsBytesOverTheIntervalThatActuallyElapsed(t *testing.T) {
	at := time.Now()
	e := sampler(t, &at, time.Minute)

	// First sight: nothing to compare against, so no rate.
	m, stalled, first := e.observe("H", 0)
	if m.rate != 0 || stalled || first {
		t.Fatalf("first observation = %+v stalled=%v first=%v, want a bare mark", m, stalled, first)
	}

	// 5 MB across 5 s is 1 MB/s.
	at = at.Add(5 * time.Second)
	m, _, _ = e.observe("H", 5<<20)
	if got, want := m.rate, int64(1<<20); got != want {
		t.Errorf("rate = %d B/s, want %d", got, want)
	}
	if e.rateOf(m) != 1<<20 {
		t.Errorf("rateOf = %d, want it reported while fresh", e.rateOf(m))
	}

	// 10 MB across 2 s is 5 MB/s — the interval is measured, not assumed.
	at = at.Add(2 * time.Second)
	m, _, _ = e.observe("H", 15<<20)
	if got, want := m.rate, int64(5<<20); got != want {
		t.Errorf("rate = %d B/s, want %d — the interval must come from the clock", got, want)
	}
}

// Two callers now ask for the torrent list: the poller on its own interval and
// GET /api/downloads on whatever Activity is doing. Observations can therefore
// land milliseconds apart, and a megabyte across 40 ms is a true instantaneous
// rate and a useless one — it reads 25 MB/s on a 3 MB/s download.
func TestAnObservationTooSoonCarriesTheRateRatherThanInventingOne(t *testing.T) {
	at := time.Now()
	e := sampler(t, &at, time.Minute)

	e.observe("H", 0)
	at = at.Add(4 * time.Second)
	m, _, _ := e.observe("H", 4<<20) // 1 MB/s
	if m.rate != 1<<20 {
		t.Fatalf("rate = %d, want 1 MB/s to start from", m.rate)
	}

	// 1 MB more, 40 ms later. Naively that is 25 MB/s.
	at = at.Add(40 * time.Millisecond)
	m, _, _ = e.observe("H", 5<<20)
	if m.rate != 1<<20 {
		t.Errorf("rate = %d B/s after a 40ms gap, want the carried 1 MB/s — "+
			"a sub-second interval must not manufacture a spike", m.rate)
	}
}

// A count that goes DOWN is a re-hash or a dropped piece, not a negative
// download.
func TestAFallingByteCountIsNotANegativeRate(t *testing.T) {
	at := time.Now()
	e := sampler(t, &at, time.Minute)

	e.observe("H", 10<<20)
	at = at.Add(5 * time.Second)
	m, _, _ := e.observe("H", 2<<20)
	if m.rate != 0 {
		t.Errorf("rate = %d B/s after the count fell, want 0", m.rate)
	}
}

// A rate outlives its bytes for rateStaleAfter and no longer. With nothing
// moving there is no new delta to take, only an increasingly old one to repeat —
// and a screen showing 4 MB/s on a download that stopped two minutes ago is
// worse than one showing nothing.
func TestARateGoesStaleBeforeTheStallDetectorFires(t *testing.T) {
	at := time.Now()
	e := sampler(t, &at, DefaultStallAfter)

	e.observe("H", 0)
	at = at.Add(5 * time.Second)
	m, _, _ := e.observe("H", 5<<20)
	if e.rateOf(m) == 0 {
		t.Fatal("rateOf = 0 immediately after a move, want the measured rate")
	}

	// Still fresh a moment later.
	at = at.Add(rateStaleAfter - time.Second)
	m, stalled, _ := e.observe("H", 5<<20)
	if e.rateOf(m) == 0 {
		t.Error("rateOf = 0 just inside the window, want the carried rate")
	}
	if stalled {
		t.Error("stalled after 29s with DefaultStallAfter of 5m")
	}

	// Past the window, and still four and a half minutes short of stalled.
	at = at.Add(2 * time.Second)
	m, stalled, _ = e.observe("H", 5<<20)
	if e.rateOf(m) != 0 {
		t.Errorf("rateOf = %d past rateStaleAfter, want 0", e.rateOf(m))
	}
	if stalled {
		t.Error("stalled at 31s; the two signals must be independent — " +
			"0 B/s at 30s, and only then `stalled` at 5m")
	}

	// And the stall clock still runs from when the bytes last moved, which the
	// rate going stale must not have disturbed.
	at = at.Add(DefaultStallAfter)
	if _, stalled, _ = e.observe("H", 5<<20); !stalled {
		t.Error("not stalled past DefaultStallAfter — extracting the rate broke the stall clock")
	}
}

// The stall warning is written once per stall, not once per poll: a five-second
// poll must not produce a five-second warning. Pulling the sampler out of
// `stalled` moved that flag, so this is the regression that would follow.
func TestTheStallIsReportedOnceAcrossManyObservations(t *testing.T) {
	at := time.Now()
	e := sampler(t, &at, time.Minute)

	e.observe("H", 100)
	at = at.Add(90 * time.Second)

	reports := 0
	for i := 0; i < 5; i++ {
		_, stalled, first := e.observe("H", 100)
		if !stalled {
			t.Fatalf("observation %d is not stalled after 90s", i)
		}
		if first {
			reports++
		}
		at = at.Add(5 * time.Second)
	}
	if reports != 1 {
		t.Errorf("firstReport was true %d times across five polls, want exactly 1", reports)
	}
}
