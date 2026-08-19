package vpn

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// State is what a check concluded, in the vocabulary a screen can render.
//
// Six rather than a boolean because the instructions differ. "Never handshaken"
// is a config to fix, "stale" is usually nothing at all, and "the exit address
// is this machine's own" is a tunnel that is up and doing nothing — the one
// state where bytes really would leave from the real address while everything
// reports healthy.
type State string

const (
	// StateOff is no tunnel configured. It is not a failure; it is what
	// VPN_REQUIRED=false is for.
	StateOff State = "off"

	// StateWaiting is configured and has never completed a handshake. The far
	// end has not answered once, so this is a credential, an endpoint or a
	// firewall.
	StateWaiting State = "waiting"

	// StateStale is handshaken, but longer ago than REJECT_AFTER_TIME. On a
	// config with PersistentKeepalive it means the peer is gone. Without one it
	// usually means nothing has needed the tunnel lately, which is why this is
	// not on its own a refusal — see Status.Fresh.
	StateStale State = "stale"

	// StateBlocked is up and handshaking and NOT changing where traffic leaves
	// from, or unable to prove that it is. Fail-closed lands here.
	StateBlocked State = "blocked"

	// StateUp is the only good one: fresh handshake, and traffic through the
	// tunnel comes out somewhere other than this machine's own address.
	StateUp State = "up"

	// StateUnknown is a device that could not be read at all.
	StateUnknown State = "unknown"
)

// Verdict is one answer to "is this tunnel carrying the traffic", with
// everything a screen or a log needs to say why.
//
// It is a value rather than an error because two different callers want two
// different things from it: dispatch wants a refusal with a sentence, and the
// sentinel wants a state it can compare against the last one to spot a
// transition. An error can only be the first.
type Verdict struct {
	State  State
	Detail string

	// At is when this verdict was reached, so a caller can say how old it is
	// rather than implying it is live.
	At time.Time

	// Status is the device read this was drawn from. Zero for StateOff.
	Status Status

	// ExitChecked says whether the expensive half ran for THIS verdict. False
	// means the exit address below came from the cache or was never asked for,
	// and a screen that draws it as freshly proved would be lying.
	ExitChecked bool

	// ExitDiffers is whether traffic through the tunnel comes out somewhere
	// other than the host's own address. It is the whole question CheckExit
	// exists to answer.
	ExitDiffers bool

	// ExitAddress is the address traffic leaves from. It is the one fact the
	// tunnel exists to keep, so it is NEVER logged and the API withholds it
	// unless a password is in front of the endpoint (docs/decisions.md D18).
	ExitAddress string

	// cause is the error the refusal came from, kept so that errors.Is still
	// answers for ErrSameExit and for a context deadline. Dispatch wraps it in
	// download.Unprotected, which reports Detail to a person and the cause to
	// errors.Is.
	cause error
}

// OK reports whether this verdict permits traffic. Only StateUp does.
//
// Everything else is fail-closed, including StateUnknown: "I could not
// establish that this is protected" and "this is not protected" have the same
// consequence for a mandatory tunnel, which is the rule internal/download
// already states at its own boundary.
func (v Verdict) OK() bool { return v.State == StateUp }

// Err is the refusal as an error, or nil when the verdict is good.
func (v Verdict) Err() error {
	if v.OK() {
		return nil
	}
	if v.cause != nil {
		return v.cause
	}
	if v.Detail == "" {
		return errors.New("the VPN tunnel could not be verified")
	}
	return errors.New(v.Detail)
}

// Checker proves a tunnel, and is the ONE implementation of what that means.
//
// It exists because there are now two callers — the dispatch guard and the
// sentinel — and two implementations of "is this tunnel good" would eventually
// disagree, at which point the screen and the refusal say different things about
// the same tunnel and neither is obviously wrong.
//
// The cheap half is a device read in this process. The expensive half is
// CheckExit: two HTTP round trips, one of them through the tunnel, against a
// third party. Only the expensive half is cached.
type Checker struct {
	tunnel   tunnelState
	checkURL string
	host     *http.Client
	ttl      time.Duration
	log      *slog.Logger

	// now is a seam for the cache tests, which must not sleep for minutes.
	now func() time.Time

	mu       sync.Mutex
	goodTill time.Time
	last     Verdict
}

// NewChecker builds one. A zero ttl takes the default; host may be nil.
func NewChecker(t tunnelState, checkURL string, host *http.Client, ttl time.Duration, log *slog.Logger) *Checker {
	if log == nil {
		log = slog.Default()
	}
	if ttl <= 0 {
		ttl = DefaultExitTTL
	}
	return &Checker{tunnel: t, checkURL: checkURL, host: host, ttl: ttl, log: log, now: time.Now}
}

// DefaultExitTTL is how long a proved exit address is taken on trust.
//
// It is the interval this has used since phase 6. What changed is what it
// covers: the device read is no longer inside it, so a cached pass now means
// "the exit address was proved recently AND the far end answered just now"
// rather than "something was fine ten minutes ago".
const DefaultExitTTL = 10 * time.Minute

// Last is the most recent verdict without asking anything. It is what a status
// endpoint serves, and it is safe to poll.
func (c *Checker) Last() Verdict {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.last
}

// Cheap re-reads the device and nothing else.
//
// It is what a watchdog runs on its short tick: a dead peer shows up here within
// one handshake window, in-process, with no third party involved. A tunnel that
// looks fine to this can still be failing the only question that matters, which
// is why it never produces StateUp on its own — the best it can say is that
// nothing is visibly wrong, and it defers to whatever the last exit check
// concluded.
func (c *Checker) Cheap() Verdict {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	status, err := c.tunnel.Status()
	switch {
	case err != nil:
		return c.record(Verdict{State: StateUnknown, At: now, cause: err,
			Detail: "the VPN device could not be read: " + err.Error()})

	case !status.Handshaken():
		return c.record(Verdict{State: StateWaiting, At: now, Status: status,
			Detail: fmt.Sprintf("the VPN tunnel has never completed a handshake with %s", status.Endpoint)})

	case !status.Fresh(now):
		return c.record(Verdict{State: StateStale, At: now, Status: status,
			Detail: fmt.Sprintf("the last handshake with %s was %s ago, longer than the %s a peer will accept a keypair for",
				status.Endpoint, status.Age(now).Round(time.Second), HandshakeStale)})
	}

	// The device is healthy. Whether traffic actually leaves somewhere else is
	// not a question this can answer, so the previous exit check stands — and
	// the verdict says it was not re-proved, so nothing draws it as live.
	previous := c.last
	verdict := Verdict{State: StateBlocked, At: now, Status: status,
		Detail: "the tunnel is up, but where its traffic leaves from has not been established yet"}
	if previous.ExitDiffers && now.Before(c.goodTill) {
		verdict.State = StateUp
		verdict.Detail = "the tunnel is up and its traffic leaves from somewhere other than this machine"
		verdict.ExitDiffers = true
		verdict.ExitAddress = previous.ExitAddress
	}
	return c.record(verdict)
}

// Check proves the tunnel, using the cached exit address when it is still good
// and the handshake is fresh.
//
// force skips the cache. It is what POST /api/vpn/check is for, and what the
// sentinel uses when the cheap read has just changed its mind.
func (c *Checker) Check(ctx context.Context, force bool) Verdict {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	status, err := c.tunnel.Status()
	switch {
	case err != nil:
		return c.record(Verdict{State: StateUnknown, At: now, cause: err,
			Detail: "the VPN device could not be read: " + err.Error()})

	case !status.Handshaken():
		// The refusal, and it is Handshaken rather than Fresh on purpose. A
		// tunnel that has never worked cannot carry anything; one that has not
		// been used lately rekeys on the first dial. See Status.Handshaken.
		return c.record(Verdict{State: StateWaiting, At: now, Status: status,
			Detail: fmt.Sprintf("the VPN tunnel has never completed a handshake with %s", status.Endpoint)})
	}

	// Fresh does not refuse; it invalidates. A handshake older than
	// REJECT_AFTER_TIME is one the far end would no longer accept traffic under,
	// so whatever the exit address was is no longer evidence about now.
	fresh := status.Fresh(now)
	if !force && fresh && now.Before(c.goodTill) && c.last.ExitDiffers {
		verdict := c.last
		verdict.At = now
		verdict.Status = status
		verdict.ExitChecked = false
		return c.record(verdict)
	}

	address, err := c.tunnel.CheckExit(ctx, c.checkURL, c.host)
	if err != nil {
		return c.record(Verdict{State: StateBlocked, At: now, Status: status,
			ExitChecked: true, ExitAddress: address, cause: err, Detail: err.Error()})
	}

	// Not logged with the address in it: GET /api/logs is readable by anyone on
	// the LAN (docs/decisions.md D18), and this is the one fact the tunnel
	// exists to keep.
	c.log.Info("vpn check passed: the tunnel is up and traffic leaves from somewhere else")
	c.goodTill = now.Add(c.ttl)
	return c.record(Verdict{State: StateUp, At: now, Status: status,
		ExitChecked: true, ExitDiffers: true, ExitAddress: address,
		Detail: "the tunnel is up and its traffic leaves from somewhere other than this machine"})
}

// record stores a verdict as the latest and returns it. Callers hold c.mu.
func (c *Checker) record(v Verdict) Verdict {
	c.last = v
	return v
}
