package vpn

import (
	"context"
	"errors"
	"testing"
	"time"
)

// sentinelFor wires a sentinel onto a fake tunnel with a pinned clock, which is
// the only way to assert a five-minute cadence in a test that finishes.
func sentinelFor(t *testing.T, tunnel *fakeTunnel, now *time.Time, active func(context.Context) bool) *Sentinel {
	t.Helper()
	c := NewChecker(tunnel, "http://example.invalid", nil, time.Hour, quiet())
	c.now = func() time.Time { return *now }
	s := NewSentinel(c, active, quiet())
	s.now = func() time.Time { return *now }
	return s
}

func healthy(now *time.Time) *fakeTunnel {
	return &fakeTunnel{
		status: Status{Endpoint: "sg701:51820", LastHandshake: *now},
		now:    func() time.Time { return *now },
	}
}

// TestTheSentinelFailsClosedOnAStatusError. A device that cannot be read is not
// a tunnel that is fine.
func TestTheSentinelFailsClosedOnAStatusError(t *testing.T) {
	now := time.Now()
	tunnel := &fakeTunnel{err: errors.New("device is closed")}
	s := sentinelFor(t, tunnel, &now, nil)

	v := s.Check(context.Background())
	if v.OK() {
		t.Error("a device that could not be read produced a good verdict")
	}
	if v.State != StateUnknown {
		t.Errorf("State = %q, want unknown", v.State)
	}
}

// TestTheSentinelFailsClosedOnErrSameExit. The tunnel being UP is not the point:
// this is the one state where bytes really do leave from the real address while
// every other indicator reads healthy.
func TestTheSentinelFailsClosedOnErrSameExit(t *testing.T) {
	now := time.Now()
	tunnel := healthy(&now)
	tunnel.failWith = ErrSameExit
	s := sentinelFor(t, tunnel, &now, nil)

	v := s.Check(context.Background())
	if v.OK() {
		t.Error("a tunnel leaving from the host's own address produced a good verdict")
	}
	if !errors.Is(v.Err(), ErrSameExit) {
		t.Errorf("err = %v, want it to still match ErrSameExit", v.Err())
	}
}

// TestTheSentinelDoesNotCheckTheExitWhileIdle is the cadence decision, and it is
// the reason an unattended box does not make 288 requests a day to somebody
// else's IP-echo service.
//
// With nothing downloading there are no bytes to stop, and a dispatch proves the
// exit before it starts anything. The cheap read keeps running the whole time,
// so a dead peer is still caught within one tick.
func TestTheSentinelDoesNotCheckTheExitWhileIdle(t *testing.T) {
	now := time.Now()
	tunnel := healthy(&now)
	idle := func(context.Context) bool { return false }
	s := sentinelFor(t, tunnel, &now, idle)

	s.Check(context.Background()) // the boot proof
	if tunnel.checks != 1 {
		t.Fatalf("exit checks after boot = %d, want 1", tunnel.checks)
	}

	// Four hours of an idle, healthy box.
	for range 4 * 60 {
		now = now.Add(time.Minute)
		tunnel.status.LastHandshake = now // a keepalive is holding it fresh
		s.tick(context.Background())
	}

	if tunnel.checks != 1 {
		t.Errorf("exit checks = %d after four idle hours, want the boot proof and nothing else", tunnel.checks)
	}
	if tunnel.reads != 241 {
		t.Errorf("device reads = %d, want one per tick plus the boot proof — the cheap read never stops", tunnel.reads)
	}
}

// TestTheSentinelChecksTheExitWhileDownloading is the other half: the silent
// degradation this exists to catch can only happen while bytes are moving, and
// that is exactly when it looks.
func TestTheSentinelChecksTheExitWhileDownloading(t *testing.T) {
	now := time.Now()
	tunnel := healthy(&now)
	busy := func(context.Context) bool { return true }
	s := sentinelFor(t, tunnel, &now, busy)

	s.Check(context.Background())

	// Half an hour of downloading, ticking every fifteen seconds.
	for range 2 * 60 {
		now = now.Add(15 * time.Second)
		tunnel.status.LastHandshake = now
		s.tick(context.Background())
	}

	// Thirty minutes at five-minute intervals is six, plus the boot proof.
	if tunnel.checks != 7 {
		t.Errorf("exit checks = %d over half an hour of downloading, want 7 (boot + one per five minutes)", tunnel.checks)
	}
}

// TestABadVerdictKeepsBeingReCheckedEvenWithNothingDownloading is the recovery
// path, and it is the one the cadence decision above would otherwise have
// broken.
//
// A held download does not report itself as downloading — it is stalled, with
// the tunnel named as the reason — so `active` is false exactly when the tunnel
// is broken. Without the !OK clause a bad verdict would switch off the only
// thing that could ever clear it, and downloads would stay held until somebody
// restarted curator.
func TestABadVerdictKeepsBeingReCheckedEvenWithNothingDownloading(t *testing.T) {
	now := time.Now()
	tunnel := healthy(&now)
	tunnel.failWith = ErrSameExit
	idle := func(context.Context) bool { return false }
	s := sentinelFor(t, tunnel, &now, idle)

	if v := s.Check(context.Background()); v.OK() {
		t.Fatal("the boot proof passed a tunnel leaving from the host's own address")
	}

	// Twenty minutes of a broken tunnel on an idle box.
	for range 80 {
		now = now.Add(15 * time.Second)
		tunnel.status.LastHandshake = now
		s.tick(context.Background())
	}
	if tunnel.checks < 4 {
		t.Errorf("exit checks = %d over twenty broken minutes, want one per five minutes: "+
			"a bad verdict must keep being re-checked or nothing can ever release the downloads", tunnel.checks)
	}

	// And when it comes back, the very next due check says so.
	tunnel.failWith = nil
	now = now.Add(SentinelExitInterval)
	tunnel.status.LastHandshake = now
	if v := s.tick(context.Background()); !v.OK() {
		t.Errorf("the tunnel recovered and the sentinel did not notice: %s", v.Detail)
	}
}

// TestABrokenTunnelIsNotHammered. A state that does not change must not force a
// check every tick — which is what measuring a transition against the last
// VERDICT rather than the last cheap read would do, at the exact moment the
// far end cannot be reached.
func TestABrokenTunnelIsNotHammered(t *testing.T) {
	now := time.Now()
	tunnel := healthy(&now)
	tunnel.failWith = errors.New("the tunnel is not carrying traffic")
	s := sentinelFor(t, tunnel, &now, func(context.Context) bool { return true })

	s.Check(context.Background())
	before := tunnel.checks

	// One minute of fifteen-second ticks, all in the same bad state.
	for range 4 {
		now = now.Add(15 * time.Second)
		tunnel.status.LastHandshake = now
		s.tick(context.Background())
	}

	if got := tunnel.checks - before; got != 0 {
		t.Errorf("%d extra exit checks in one minute of an unchanged bad state; "+
			"a transition is measured against the previous cheap read, not the last verdict", got)
	}
}

// TestSubscribersFireOnTransitionsRatherThanOnTicks. Fifteen seconds forever is
// a log line nobody reads, and a log nobody reads hides the one that mattered.
func TestSubscribersFireOnTransitionsRatherThanOnTicks(t *testing.T) {
	now := time.Now()
	tunnel := healthy(&now)
	s := sentinelFor(t, tunnel, &now, func(context.Context) bool { return false })

	var got []State
	s.Subscribe(func(v Verdict) { got = append(got, v.State) })

	s.Check(context.Background()) // up
	for range 10 {
		now = now.Add(15 * time.Second)
		tunnel.status.LastHandshake = now
		s.tick(context.Background())
	}
	if len(got) != 1 {
		t.Fatalf("subscriber fired %d times for one steady state: %v", len(got), got)
	}

	// The peer goes away: no more handshakes, the clock moves past
	// REJECT_AFTER_TIME, and traffic through the tunnel stops arriving. All
	// three, because a stale handshake on its own is an idle tunnel and this
	// test is about a broken one.
	tunnel.failWith = errors.New("the tunnel is not carrying traffic")
	for range 20 {
		now = now.Add(15 * time.Second)
		s.tick(context.Background())
	}
	if len(got) != 2 {
		t.Fatalf("subscriber fired %d times across one failure: %v", len(got), got)
	}
	if got[0] != StateUp || got[1] == StateUp {
		t.Errorf("transitions = %v, want up then something that is not", got)
	}

	// And back: the peer returns and the exit proves out again.
	tunnel.failWith = nil
	tunnel.status.LastHandshake = now
	now = now.Add(15 * time.Second)
	s.tick(context.Background())
	if len(got) != 3 || got[2] != StateUp {
		t.Errorf("transitions = %v, want a third one back to up", got)
	}
}

// TestADeadPeerIsNoticedInProcess. The cheap read is what bounds the worst case
// for a dead peer at one tick plus a handshake window, and it does it with a
// device read in this process — so it still works when the tunnel is the thing
// that is broken and nothing external can be reached.
//
// What it produces is a RE-PROVE, not a verdict. A stale handshake is not by
// itself proof of anything: if the exit check then succeeds, traffic did go
// through the tunnel and the tunnel is fine, which is the idle-tunnel case the
// dispatch refusal is careful about too. Only a stale handshake AND a failed
// exit check is a dead peer.
func TestADeadPeerIsNoticedInProcess(t *testing.T) {
	now := time.Now()
	tunnel := healthy(&now)
	s := sentinelFor(t, tunnel, &now, func(context.Context) bool { return false })

	s.Check(context.Background())
	exitChecks := tunnel.checks

	// The peer stops answering, but the tunnel still carries traffic: the check
	// succeeds and forced it to rekey. This is a working install whose provider
	// config has no PersistentKeepalive, and it must not be reported as broken.
	now = now.Add(HandshakeStale + time.Second)
	v := s.tick(context.Background())
	if tunnel.checks == exitChecks {
		t.Fatal("a stale handshake did not force a re-prove")
	}
	if !v.OK() {
		t.Errorf("an idle tunnel that still carries traffic was reported broken: %s", v.Detail)
	}

	// Now it is genuinely gone.
	tunnel.failWith = errors.New("the tunnel is not carrying traffic")
	tunnel.status.LastHandshake = now.Add(-HandshakeStale - time.Minute)
	now = now.Add(SentinelInterval)
	if v := s.tick(context.Background()); v.OK() {
		t.Error("a stale handshake with a failing exit check still produced a good verdict")
	}
}
