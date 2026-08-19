package vpn

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// at pins a checker's clock so the cache can be tested without sleeping.
func at(c *Checker, t *time.Time) { c.now = func() time.Time { return *t } }

// TestACheckerSaysWhichOfTheSixThingsIsWrong. The states are not decoration:
// each one has a different instruction attached, and a boolean would have to
// pick one sentence for all of them.
func TestACheckerSaysWhichOfTheSixThingsIsWrong(t *testing.T) {
	now := time.Now()

	for name, tc := range map[string]struct {
		tunnel *fakeTunnel
		want   State
		says   string
	}{
		"the device will not answer": {
			tunnel: &fakeTunnel{err: errors.New("device is closed")},
			want:   StateUnknown,
			says:   "device is closed",
		},
		"the far end has never answered": {
			tunnel: &fakeTunnel{status: Status{Endpoint: "sg701:51820"}},
			want:   StateWaiting,
			says:   "sg701:51820",
		},
		"the exit address is the host's own": {
			tunnel: &fakeTunnel{
				status:   Status{Endpoint: "sg701:51820", LastHandshake: now},
				failWith: ErrSameExit,
			},
			want: StateBlocked,
			says: "exit address",
		},
		"everything is as it should be": {
			tunnel: &fakeTunnel{status: Status{Endpoint: "sg701:51820", LastHandshake: now}},
			want:   StateUp,
			says:   "leaves from somewhere other than this machine",
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := NewChecker(tc.tunnel, "http://example.invalid", nil, 0, quiet())
			at(c, &now)

			v := c.Check(context.Background(), false)
			if v.State != tc.want {
				t.Errorf("State = %q, want %q (detail: %s)", v.State, tc.want, v.Detail)
			}
			if !strings.Contains(v.Detail, tc.says) {
				t.Errorf("Detail = %q, want it to mention %q", v.Detail, tc.says)
			}
			if v.OK() != (tc.want == StateUp) {
				t.Errorf("OK = %v for state %q", v.OK(), v.State)
			}
			if got := c.Last(); got.State != v.State {
				t.Errorf("Last().State = %q, want the verdict just reached (%q)", got.State, v.State)
			}
		})
	}
}

// TestEveryVerdictThatIsNotUpIsFailClosed is the rule stated once and checked,
// rather than four `case` arms that each remembered it.
//
// StateUnknown is the one worth naming: "I could not establish that this is
// protected" and "this is not protected" have the same consequence for a
// mandatory tunnel, which is what internal/download already says at its own
// boundary.
func TestEveryVerdictThatIsNotUpIsFailClosed(t *testing.T) {
	for _, state := range []State{StateOff, StateWaiting, StateStale, StateBlocked, StateUnknown} {
		v := Verdict{State: state, Detail: "because"}
		if v.OK() {
			t.Errorf("%s reports OK; only up may", state)
		}
		if v.Err() == nil {
			t.Errorf("%s produced no error, so a dispatch would proceed on it", state)
		}
	}
	up := Verdict{State: StateUp}
	if !up.OK() || up.Err() != nil {
		t.Errorf("up reports OK=%v err=%v", up.OK(), up.Err())
	}
}

// TestTheCauseSurvivesTheVerdict. download.UnprotectedFor keeps the guard's
// error as the cause so errors.Is still answers — a check that timed out has to
// keep matching context.DeadlineExceeded, and a same-exit refusal has to keep
// matching ErrSameExit, or the sentinel cannot tell them apart either.
func TestTheCauseSurvivesTheVerdict(t *testing.T) {
	now := time.Now()
	c := NewChecker(&fakeTunnel{
		status:   Status{Endpoint: "sg701:51820", LastHandshake: now},
		failWith: ErrSameExit,
	}, "http://example.invalid", nil, 0, quiet())
	at(c, &now)

	err := c.Check(context.Background(), false).Err()
	if !errors.Is(err, ErrSameExit) {
		t.Errorf("err = %v, want it to still match ErrSameExit", err)
	}
}

// TestOnlyTheExpensiveHalfIsCached is the shape T82 established, asserted on the
// Checker now that it owns the caching.
func TestOnlyTheExpensiveHalfIsCached(t *testing.T) {
	now := time.Now()
	tunnel := &fakeTunnel{status: Status{Endpoint: "sg701:51820", LastHandshake: now}}
	c := NewChecker(tunnel, "http://example.invalid", nil, time.Hour, quiet())
	at(c, &now)

	if v := c.Check(context.Background(), false); !v.OK() {
		t.Fatalf("first check: %s", v.Detail)
	}
	if tunnel.checks != 1 || tunnel.reads != 1 {
		t.Fatalf("after one check: %d exit checks, %d device reads; want 1 and 1", tunnel.checks, tunnel.reads)
	}

	// Six films dispatched in a row: one round trip to the IP-echo service, six
	// device reads. That split is the whole design.
	for range 5 {
		if v := c.Check(context.Background(), false); !v.OK() {
			t.Fatalf("cached check: %s", v.Detail)
		}
	}
	if tunnel.checks != 1 {
		t.Errorf("exit checks = %d after six dispatches, want the cache to have answered five", tunnel.checks)
	}
	if tunnel.reads != 6 {
		t.Errorf("device reads = %d after six dispatches, want one each — the cheap half is never cached", tunnel.reads)
	}

	// force is what POST /api/vpn/check is for.
	if v := c.Check(context.Background(), true); !v.OK() || !v.ExitChecked {
		t.Errorf("a forced check did not re-prove the exit address: checked=%v", v.ExitChecked)
	}
	if tunnel.checks != 2 {
		t.Errorf("exit checks = %d after a forced one, want 2", tunnel.checks)
	}
}

// TestACachedVerdictSaysItWasNotReProved. Without this a screen polling every
// three seconds draws a ten-minute-old exit address as if it had just been
// established, which is the same lie the ten-minute hole told.
func TestACachedVerdictSaysItWasNotReProved(t *testing.T) {
	now := time.Now()
	c := NewChecker(&fakeTunnel{status: Status{Endpoint: "sg701:51820", LastHandshake: now}},
		"http://example.invalid", nil, time.Hour, quiet())
	at(c, &now)

	if first := c.Check(context.Background(), false); !first.ExitChecked {
		t.Fatal("the first check reports it did not check the exit address")
	}
	second := c.Check(context.Background(), false)
	if second.ExitChecked {
		t.Error("a cached verdict claims the exit address was checked for it")
	}
	if !second.ExitDiffers || second.ExitAddress == "" {
		t.Error("a cached verdict lost what was actually proved; it should carry it, just not claim it is fresh")
	}
}

// TestTheCheapReadNeverProvesTheThingItCannotSee.
//
// Cheap re-reads the device and nothing else, which is what makes it affordable
// every fifteen seconds. A healthy device is not evidence that traffic leaves
// somewhere else — that is the one state where bytes really would go out of the
// real address while everything on screen looked fine — so it may never conclude
// StateUp on its own.
func TestTheCheapReadNeverProvesTheThingItCannotSee(t *testing.T) {
	now := time.Now()
	tunnel := &fakeTunnel{status: Status{Endpoint: "sg701:51820", LastHandshake: now}}
	c := NewChecker(tunnel, "http://example.invalid", nil, time.Hour, quiet())
	at(c, &now)

	v := c.Cheap()
	if v.State == StateUp {
		t.Error("the cheap read concluded the tunnel is up without ever asking where traffic comes out")
	}
	if v.State != StateBlocked {
		t.Errorf("State = %q, want blocked until an exit check has happened", v.State)
	}
	if tunnel.checks != 0 {
		t.Errorf("the cheap read made %d exit checks; it must make none", tunnel.checks)
	}

	// Once the exit HAS been proved, the cheap read defers to it rather than
	// re-litigating — otherwise a fifteen-second tick would undo every proof.
	if !c.Check(context.Background(), false).OK() {
		t.Fatal("the full check did not pass")
	}
	if v := c.Cheap(); !v.OK() {
		t.Errorf("the cheap read overrode a good exit check: %s", v.Detail)
	} else if v.ExitChecked {
		t.Error("the cheap read claims it checked the exit address")
	}

	// And a dead peer takes it away again, within one handshake window and
	// without a third party being involved at all.
	now = now.Add(HandshakeStale + time.Minute)
	if v := c.Cheap(); v.State != StateStale {
		t.Errorf("State = %q after the handshake went stale, want stale", v.State)
	}
	if tunnel.checks != 1 {
		t.Errorf("exit checks = %d; the cheap read must never make one", tunnel.checks)
	}
}
