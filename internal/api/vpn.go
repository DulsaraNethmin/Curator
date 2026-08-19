package api

import (
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/DulsaraNethmin/curator/internal/vpn"
)

// The VPN screen's two endpoints (docs/tasks/T84).
//
// Modelled on update.go rather than indexers.go: a Setup struct of concrete
// dependencies, a With to attach it, and a Register to mount it. The contract is
// the one every probe in this package follows — ALWAYS 200, because the state is
// the answer. A process with no wiring at all is the single 503, and that is a
// deployment fact rather than a tunnel state.

// TunnelOwner answers whether an address is one the tunnel holds. *vpn.Tunnel
// satisfies it.
type TunnelOwner interface {
	Owns(addr net.Addr) bool
}

// VPNSetup is what the VPN screen needs behind it.
type VPNSetup struct {
	// Sentinel is the watchdog. Nil is a supported state and it is what every
	// install without the embedded engine is in: an external qBittorrent is not
	// curator's socket to watch (docs/decisions.md D27), so the screen reports
	// what enforcement is configured and says plainly that it can promise
	// nothing else.
	Sentinel *vpn.Sentinel

	// Tunnel answers whether an address belongs to it, and nothing else.
	//
	// An interface rather than *vpn.Tunnel for the reason MinterProber is one:
	// what the handler can reach is then exactly what it needs. This one can
	// answer a question about an address; it cannot dial, listen, resolve or
	// read a key. Nil when no tunnel is configured.
	Tunnel TunnelOwner

	// Binding is the address the engine's socket actually holds, read off the
	// engine rather than remembered from the wiring. Nil for a process with no
	// embedded engine — including, deliberately, one that refused to build an
	// engine because there was no tunnel.
	Binding func() net.Addr

	// Held reports whether downloads are stopped and why.
	Held func() (bool, string)

	// Enforcing reports whether VPN_REQUIRED is on right now.
	Enforcing func() bool

	// Editable is false when the environment owns VPN_REQUIRED, which wins over
	// the settings table. Nil counts as editable. Without it the screen draws a
	// switch that answers 409 — the settings screen has rendered shadowed
	// fields read-only since phase 7, and a control anywhere else has to know
	// the same fact.
	Editable func() bool

	// Backend is "embedded" or "qbittorrent", and Tunnelled says whether this
	// process's engine was built bound to a tunnel. Together they are the scope
	// of the guarantee, which the page has to state rather than let a reader
	// generalise from a green badge.
	Backend   string
	Tunnelled bool

	// Auth gates the exit address. Nil means no authentication in this process,
	// which withholds it — the safe direction.
	Auth *Auth
}

// WithVPN attaches the VPN screen's dependencies.
func (s *Server) WithVPN(setup VPNSetup) *Server {
	s.vpn = &setup
	return s
}

// RegisterVPN mounts the VPN screen's routes.
func (s *Server) RegisterVPN(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/vpn", s.handleVPN)
	mux.HandleFunc("POST /api/vpn/check", s.handleVPNCheck)
}

// vpnCheckEvery rate-limits the forced re-prove.
//
// POST /api/vpn/check makes two HTTP round trips to a third party, one of them
// through the tunnel, and it is on a button somebody will press repeatedly while
// watching a tunnel come up. Thirty seconds is longer than the 20 s budget one
// check is allowed, so a held button cannot queue them either.
const vpnCheckEvery = 30 * time.Second

// vpnBody is what GET /api/vpn answers.
type vpnBody struct {
	// State is vpn.State: off, waiting, stale, blocked, up or unknown.
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`

	// Protected is the headline, and it is deliberately NOT just `state == up`.
	// It is every fact the page's verdict rests on, ANDed here so the screen
	// cannot draw a green banner off one of them.
	Protected bool `json:"protected"`

	// Endpoint is the PEER's address — the VPN server — which is a fact about
	// which provider is in use and not about where this machine's traffic comes
	// out. The one that matters is Exit, below, and it is gated.
	Endpoint string `json:"endpoint,omitempty"`

	HandshakeAgeSeconds *int64 `json:"handshake_age_seconds,omitempty"`
	UptimeSeconds       *int64 `json:"uptime_seconds,omitempty"`
	Received            int64  `json:"received"`
	Sent                int64  `json:"sent"`
	Keepalive           int    `json:"keepalive"`

	Enforcement vpnEnforcementBody `json:"enforcement"`
	Engine      vpnEngineBody      `json:"engine"`
	Hold        vpnHoldBody        `json:"hold"`
	Check       vpnCheckBody       `json:"check"`
}

type vpnEnforcementBody struct {
	// Required is VPN_REQUIRED as it applies right now.
	Required bool `json:"required"`

	// Editable is false when the environment owns it. A write would answer 409,
	// so the screen draws the switch read-only and says which variable to unset.
	Editable bool `json:"editable"`

	// EngineStarted is false when this process refused to build an engine at
	// all — no tunnel, enforcement on. Turning enforcement off then needs a
	// restart, because the toggle applies to the check and cannot conjure an
	// engine, and the screen has to say so rather than leave somebody clicking.
	EngineStarted bool `json:"engine_started"`
}

type vpnEngineBody struct {
	Backend string `json:"backend"`

	// Tunnelled is what the wiring intended; InsideTunnel is what is true.
	// Both, because a disagreement between them is the interesting case and
	// reporting only one of them hides it.
	Tunnelled    bool   `json:"tunnelled"`
	Socket       string `json:"socket,omitempty"`
	InsideTunnel bool   `json:"inside_tunnel"`
}

type vpnHoldBody struct {
	Held   bool   `json:"held"`
	Reason string `json:"reason,omitempty"`
}

type vpnCheckBody struct {
	// Checked is whether an exit check has ever run in this process.
	Checked bool `json:"checked"`

	// Fresh is whether the verdict on screen came from one, as opposed to
	// standing on an earlier one. Without it a page polling every three seconds
	// draws a ten-minute-old proof as though it had just happened.
	Fresh bool `json:"fresh"`

	// Different is whether traffic through the tunnel comes out somewhere other
	// than this machine's own address. It is the question the whole check
	// exists to answer, and it is safe to publish because it is a boolean.
	Different bool `json:"different"`

	// Masked is always present; Address only behind a password (D18). Where
	// traffic leaves from is the one fact the tunnel exists to keep, and this
	// endpoint has no authentication in front of it by default.
	Masked  string `json:"masked,omitempty"`
	Address string `json:"address,omitempty"`

	// AtSeconds is how long ago the verdict was reached.
	AtSeconds *int64 `json:"at_seconds,omitempty"`
}

func (s *Server) handleVPN(w http.ResponseWriter, r *http.Request) {
	if s.vpn == nil {
		s.fail(w, http.StatusServiceUnavailable, errors.New("this process has no VPN wiring"))
		return
	}
	s.respond(w, http.StatusOK, s.vpnBody(s.vpn.last()))
}

// handleVPNCheck forces a re-prove.
//
// It answers the same body as GET, so the screen needs no second shape for it,
// and it answers 200 even when the check fails — a tunnel that is down is what
// this endpoint is FOR, and reporting that as an error would make the page's
// failure path a network-tab mystery instead of a sentence on screen.
func (s *Server) handleVPNCheck(w http.ResponseWriter, r *http.Request) {
	if s.vpn == nil || s.vpn.Sentinel == nil {
		s.fail(w, http.StatusServiceUnavailable, errors.New("this process has no VPN to check"))
		return
	}

	if wait, ok := s.allowVPNCheck(time.Now()); !ok {
		// 429 with the wait in it. A refusal that does not say how long is a
		// button somebody keeps pressing.
		s.fail(w, http.StatusTooManyRequests, errors.New(
			"the VPN was checked a moment ago; it makes two round trips through the tunnel, "+
				"so it is rate-limited — try again in "+wait.Round(time.Second).String()))
		return
	}

	s.respond(w, http.StatusOK, s.vpnBody(s.vpn.Sentinel.Check(r.Context())))
}

// vpnBody assembles the answer from a verdict plus the four facts the page's
// headline rests on. It is one function so that GET and POST cannot drift.
func (s *Server) vpnBody(v vpn.Verdict) vpnBody {
	setup := s.vpn

	body := vpnBody{
		State:     string(v.State),
		Detail:    v.Detail,
		Endpoint:  v.Status.Endpoint,
		Received:  v.Status.Received,
		Sent:      v.Status.Sent,
		Keepalive: v.Status.Keepalive,
		Enforcement: vpnEnforcementBody{
			Required:      setup.Enforcing != nil && setup.Enforcing(),
			Editable:      setup.Editable == nil || setup.Editable(),
			EngineStarted: setup.Binding != nil,
		},
		Engine: vpnEngineBody{Backend: setup.Backend, Tunnelled: setup.Tunnelled},
		Check: vpnCheckBody{
			Checked:   v.ExitAddress != "" || v.ExitChecked,
			Fresh:     v.ExitChecked,
			Different: v.ExitDiffers,
		},
	}
	if setup.Sentinel == nil {
		body.State = string(vpn.StateOff)
		if body.Detail == "" {
			body.Detail = "no tunnel is configured in this process"
		}
	}

	if !v.Status.LastHandshake.IsZero() {
		age := int64(v.Status.Age(time.Now()).Seconds())
		body.HandshakeAgeSeconds = &age
	}
	if !v.Status.Since.IsZero() {
		up := int64(time.Since(v.Status.Since).Seconds())
		body.UptimeSeconds = &up
	}
	if !v.At.IsZero() {
		since := int64(time.Since(v.At).Seconds())
		body.Check.AtSeconds = &since
	}

	// The exit address, gated. Masked always, full only behind a password.
	if v.ExitAddress != "" {
		body.Check.Masked = maskAddress(v.ExitAddress)
		if setup.Auth != nil && setup.Auth.Enforcing() {
			body.Check.Address = v.ExitAddress
		}
	}

	if setup.Held != nil {
		held, why := setup.Held()
		body.Hold = vpnHoldBody{Held: held, Reason: why}
	}

	// The socket, read off the engine, and whether the tunnel owns it. Two
	// independent reads that have to agree; neither is a claim made by the code
	// that wired them together.
	if setup.Binding != nil {
		if addr := setup.Binding(); addr != nil {
			body.Engine.Socket = addr.String()
			body.Engine.InsideTunnel = setup.Tunnel != nil && setup.Tunnel.Owns(addr)
		}
	}

	// The headline, and every conjunct is a fact somebody else established.
	// Written as an AND rather than as `state == up` so that a page cannot go
	// green on the tunnel alone while the engine's socket sits outside it.
	body.Protected = v.OK() &&
		body.Engine.InsideTunnel &&
		body.Check.Different &&
		!body.Hold.Held
	return body
}

// maskAddress keeps the shape and throws away the identity.
//
// 203.0.113.x rather than the real last octet: enough to see that two addresses
// differ and that one changed, and not enough to say where anybody is. The
// documentation range is used on purpose so a masked address can never be
// mistaken for a real one somebody should go and look up.
func maskAddress(address string) string {
	ip := net.ParseIP(address)
	if ip == nil {
		return ""
	}
	if v4 := ip.To4(); v4 != nil {
		return "203.0.113.x"
	}
	// v6: the documentation prefix, for the same reason.
	return "2001:db8::x"
}

// last is the sentinel's latest verdict, or the off state when there is none.
func (v *VPNSetup) last() vpn.Verdict {
	if v.Sentinel == nil {
		return vpn.Verdict{State: vpn.StateOff,
			Detail: "no tunnel is configured in this process"}
	}
	return v.Sentinel.Last()
}

// allowVPNCheck is the rate limiter for the forced check. It reports how long is
// left when it refuses, so the refusal can say it.
//
// The state lives on the Server rather than in VPNSetup for the reason
// streamWarned does: a Setup is a value passed to With*, so a mutex in one would
// be copied, and a limiter that resets per copy is not a limiter.
func (s *Server) allowVPNCheck(now time.Time) (time.Duration, bool) {
	s.vpnCheckMu.Lock()
	defer s.vpnCheckMu.Unlock()
	if wait := vpnCheckEvery - now.Sub(s.vpnLastCheck); !s.vpnLastCheck.IsZero() && wait > 0 {
		return wait, false
	}
	s.vpnLastCheck = now
	return 0, true
}
