package vpn

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// The two intervals, and the difference between them is the whole design.
const (
	// SentinelInterval is the cheap tick: one IPC to a device in this process,
	// no third party, no network. It bounds how long a dead peer can go
	// unnoticed at roughly this plus one handshake window.
	SentinelInterval = 15 * time.Second

	// SentinelExitInterval is the expensive one: two HTTP round trips, one of
	// them through the tunnel, to somebody else's IP-echo service. It is what
	// catches the only failure the cheap read cannot see — a tunnel that is up
	// and handshaking and no longer changing where traffic leaves from.
	SentinelExitInterval = 5 * time.Minute
)

// Sentinel is the answer to "nobody is looking at the screen".
//
// It re-proves the tunnel on a timer and reports a verdict. It does not decide
// what to do about one: that is the caller's, because internal/vpn must not
// import the engine, and because "hold the downloads" is a policy while "the
// tunnel is not carrying this" is a fact.
type Sentinel struct {
	checker *Checker
	log     *slog.Logger

	// active reports whether anything is actually downloading. Nil counts as
	// never — a process with no torrent client has nothing to protect.
	active func(context.Context) bool

	every     time.Duration
	exitEvery time.Duration
	now       func() time.Time

	mu sync.Mutex
	// lastCheap is the previous tick's cheap state, and it is what a transition
	// is measured against — NOT the last full verdict. Measured against the
	// verdict, a tunnel sitting in one bad state would look like a fresh
	// transition on every tick and force an exit check every fifteen seconds at
	// exactly the moment the exit cannot be reached.
	lastCheap State
	lastExit  time.Time

	// announced is the state subscribers were last told about, and it is
	// separate from lastCheap on purpose. lastCheap answers "did the device
	// change" and drives whether to re-prove; this answers "does anybody need
	// telling" and is measured on the FINAL verdict. Folded into one field, a
	// cheap read and the full check that follows it disagree by construction —
	// the cheap read cannot conclude `up` — and every tick looks like a
	// transition.
	announced State

	subs []func(Verdict)
}

// NewSentinel builds one. active may be nil.
func NewSentinel(c *Checker, active func(context.Context) bool, log *slog.Logger) *Sentinel {
	if log == nil {
		log = slog.Default()
	}
	return &Sentinel{
		checker: c, log: log, active: active,
		every: SentinelInterval, exitEvery: SentinelExitInterval, now: time.Now,
	}
}

// Last is the most recent verdict, without asking anything. Safe to poll.
func (s *Sentinel) Last() Verdict { return s.checker.Last() }

// Subscribe registers a callback for STATE CHANGES, not for every tick.
//
// Ticking is every fifteen seconds for the life of the process; a subscriber
// that ran each time would be a log line every fifteen seconds saying nothing
// changed, which is a log nobody reads and therefore a log that hides the line
// that mattered. Callbacks run on the sentinel's goroutine, so a slow one delays
// the next tick.
func (s *Sentinel) Subscribe(fn func(Verdict)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs = append(s.subs, fn)
}

// Check forces a full re-prove and notifies subscribers if the state moved. It
// is what POST /api/vpn/check calls.
func (s *Sentinel) Check(ctx context.Context) Verdict {
	verdict := s.checker.Check(ctx, true)

	s.mu.Lock()
	s.lastExit = s.now()
	// The cheap view moves with it. Check and Cheap now reach the same state for
	// the same tunnel — that is a property the two are written to hold, not a
	// coincidence — so recording the verdict here keeps the next tick from
	// reading a transition that did not happen.
	s.lastCheap = verdict.State
	s.mu.Unlock()

	return s.apply(verdict)
}

// apply stores a verdict and tells subscribers if, and only if, the state moved.
// It is the one place that decides whether anything is announced.
func (s *Sentinel) apply(v Verdict) Verdict {
	s.mu.Lock()
	changed := v.State != s.announced
	s.announced = v.State
	subs := s.subs
	s.mu.Unlock()

	if changed {
		s.announce(v, subs)
	}
	return v
}

// Run proves the tunnel once, then keeps proving it until ctx is done.
//
// One goroutine and one ticker, owned by cmd/curator and stopped with the
// process. The first proof is immediate rather than one interval in: a curator
// that boots into a broken tunnel must not spend the first fifteen seconds
// reporting nothing.
func (s *Sentinel) Run(ctx context.Context) {
	s.Check(ctx)

	ticker := time.NewTicker(s.every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

// tick is one round, split out of Run so the cadence can be tested without
// waiting five minutes for it.
func (s *Sentinel) tick(ctx context.Context) Verdict {
	verdict := s.checker.Cheap()

	s.mu.Lock()
	now := s.now()
	// lastCheap records what the CHEAP read said, and nothing else writes it.
	// A full check reaches conclusions the cheap read cannot — it runs even on a
	// stale device — so storing a verdict here makes the next cheap read differ
	// from it by construction and forces an exit check every fifteen seconds at
	// exactly the moment the exit is unreachable.
	//
	// The empty state is "nothing observed yet", not a transition: the first tick
	// after boot must not re-prove a tunnel that was proved a moment ago.
	changed := s.lastCheap != "" && verdict.State != s.lastCheap

	// The expensive half, and the two conditions are not the same one written
	// twice.
	//
	// A change is a change: whatever the cheap read just noticed, the exit
	// address is worth re-establishing, in either direction. Measured against
	// the PREVIOUS CHEAP STATE, so a tunnel sitting in one bad state forces
	// exactly one check rather than one every fifteen seconds.
	//
	// The interval is the silent-degradation case, and it is deliberately not
	// unconditional. With nothing downloading there are no bytes to stop, and a
	// dispatch proves the exit before it starts anything, so an idle box makes
	// no requests at all rather than 288 a day to somebody else's service. The
	// `!OK` half is what makes recovery possible: a HELD download does not
	// report itself active, so without it a bad verdict would switch off the
	// only thing that could ever clear it.
	due := now.Sub(s.lastExit) >= s.exitEvery
	stillBad := !s.checker.Last().OK()
	s.lastCheap = verdict.State
	s.mu.Unlock()

	if changed || (due && (stillBad || s.downloading(ctx))) {
		return s.Check(ctx)
	}
	return s.apply(verdict)
}

func (s *Sentinel) downloading(ctx context.Context) bool {
	return s.active != nil && s.active(ctx)
}

// announce logs the change once and hands it to every subscriber.
//
// The address is never in it: GET /api/logs is readable by anyone on the LAN
// (docs/decisions.md D18), and where traffic leaves from is the one fact the
// tunnel exists to keep.
func (s *Sentinel) announce(v Verdict, subs []func(Verdict)) {
	if v.OK() {
		s.log.Info("vpn is protecting downloads again", "state", string(v.State), "detail", v.Detail)
	} else {
		s.log.Warn("vpn is no longer protecting downloads", "state", string(v.State), "detail", v.Detail)
	}
	for _, fn := range subs {
		fn(v)
	}
}
