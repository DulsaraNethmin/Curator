package api

// The VPN screen's two endpoints.
//
// Hermetic: internal/vpn already proves the tunnel and the sentinel against
// their own fakes, and proving them again from here would test that package's
// logic from a place that cannot see it. What is asserted here is what this
// package owns — that every tunnel state is a 200, that the headline is an AND
// of four independently-read facts rather than a restatement of one, that the
// forced check is rate-limited, and above all that the exit address does not
// leave this process without a password in front of it.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DulsaraNethmin/curator/internal/vpn"
)

func quietVPN() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeDevice satisfies what vpn.NewChecker needs. The interface is unexported
// there, which does not stop this: the methods are exported, so any type with
// them fits.
type fakeDevice struct {
	status   vpn.Status
	err      error
	exit     string
	failWith error
	checks   int
}

func (f *fakeDevice) Status() (vpn.Status, error) { return f.status, f.err }

func (f *fakeDevice) CheckExit(context.Context, string, *http.Client) (string, error) {
	f.checks++
	if f.failWith != nil {
		return "", f.failWith
	}
	f.status.LastHandshake = time.Now()
	return f.exit, nil
}

// vpnFixture is a server with the VPN screen wired to fakes.
type vpnFixture struct {
	handler http.Handler
	device  *fakeDevice
	setup   *VPNSetup
	server  *Server
}

// ownsTunnel is a tunnel that answers for one address, which is the whole of
// what this package asks a tunnel.
type ownsTunnel struct{ ip string }

func (o ownsTunnel) Owns(addr net.Addr) bool {
	if addr == nil {
		return false
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		host = addr.String()
	}
	return host == o.ip
}

type tunnelAddr struct{ ip string }

func (t tunnelAddr) Network() string { return "udp" }
func (t tunnelAddr) String() string  { return t.ip + ":51820" }

func newVPNFixture(t *testing.T, mutate func(*VPNSetup, *fakeDevice)) *vpnFixture {
	t.Helper()

	device := &fakeDevice{
		status: vpn.Status{Endpoint: "sg701.nordvpn.com:51820", LastHandshake: time.Now(),
			Received: 4096, Sent: 2048, Keepalive: 25, Since: time.Now().Add(-2 * time.Hour)},
		exit: "187.15.102.106",
	}
	checker := vpn.NewChecker(device, "http://example.invalid", nil, time.Hour, quietVPN())
	sentinel := vpn.NewSentinel(checker, nil, quietVPN())

	setup := VPNSetup{
		Sentinel:  sentinel,
		Tunnel:    ownsTunnel{"10.5.0.2"},
		Binding:   func() net.Addr { return tunnelAddr{"10.5.0.2"} },
		Held:      func() (bool, string) { return false, "" },
		Enforcing: func() bool { return true },
		Backend:   "embedded",
		Tunnelled: true,
	}
	if mutate != nil {
		mutate(&setup, device)
	}

	srv := New(nil, nil, nil, t.TempDir(), quietVPN()).WithVPN(setup)
	mux := http.NewServeMux()
	srv.RegisterVPN(mux)

	// The boot proof, so GET has something to report — the same thing
	// Sentinel.Run does before its first tick.
	sentinel.Check(context.Background())

	return &vpnFixture{handler: mux, device: device, setup: srv.vpn, server: srv}
}

func (f *vpnFixture) get(t *testing.T) (int, map[string]any) {
	t.Helper()
	return f.do(t, http.MethodGet, "/api/vpn")
}

func (f *vpnFixture) do(t *testing.T, method, path string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(method, path, nil))

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s %s: body %q is not JSON: %v", method, path, rec.Body.String(), err)
	}
	return rec.Code, body
}

// TestTheExitAddressIsWithheldUnlessAPasswordIsInFrontOfIt is the most important
// test on this screen.
//
// GET /api/vpn has no authentication in front of it by default, exactly like
// GET /api/logs, and where this machine's traffic leaves from is the single
// fact the tunnel exists to keep (docs/decisions.md D18). The masked form and
// the boolean are always safe; the address itself is not, and "nobody would
// point a browser at it" is not a control.
func TestTheExitAddressIsWithheldUnlessAPasswordIsInFrontOfIt(t *testing.T) {
	t.Run("with no password, the address never leaves the process", func(t *testing.T) {
		f := newVPNFixture(t, nil)

		status, body := f.get(t)
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		check := body["check"].(map[string]any)

		if got, ok := check["address"]; ok {
			t.Errorf("address = %v with no authentication configured; it must be absent", got)
		}
		if check["masked"] != "203.0.113.x" {
			t.Errorf("masked = %v, want the documentation-range form", check["masked"])
		}
		if check["different"] != true {
			t.Error("different = false; the boolean is the whole answer and is always safe to publish")
		}
		// Belt and braces: the real address must not appear ANYWHERE in the
		// body, not just under the field this test reads.
		raw, _ := json.Marshal(body)
		if strings.Contains(string(raw), "187.15.102.106") {
			t.Errorf("the exit address appears in the response body: %s", raw)
		}
	})

	t.Run("with a password being enforced, it is reported", func(t *testing.T) {
		auth := NewAuth([]byte("key"), func(context.Context) (Credential, error) {
			return Credential{Enabled: true, Password: "secret"}, nil
		}, quietVPN())
		if err := auth.Reload(context.Background()); err != nil {
			t.Fatalf("Reload: %v", err)
		}

		f := newVPNFixture(t, func(s *VPNSetup, _ *fakeDevice) { s.Auth = auth })

		_, body := f.get(t)
		check := body["check"].(map[string]any)
		if check["address"] != "187.15.102.106" {
			t.Errorf("address = %v behind a password, want the real one — withholding it there "+
				"leaves the person who set the password unable to see what they are protecting",
				check["address"])
		}
		if check["masked"] != "203.0.113.x" {
			t.Error("the masked form disappeared once the real one was available; both are reported")
		}
	})

	t.Run("a password that is configured but not enforced still withholds it", func(t *testing.T) {
		// The state auth.install refuses to enforce: switched on with nothing to
		// log in with. Enforcing() is the LIVE answer, so this is withheld —
		// reading the stored setting instead would publish the address in a
		// state where anybody on the LAN can still read everything else.
		auth := NewAuth([]byte("key"), func(context.Context) (Credential, error) {
			return Credential{Enabled: true}, nil
		}, quietVPN())
		if err := auth.Reload(context.Background()); err != nil {
			t.Fatalf("Reload: %v", err)
		}

		f := newVPNFixture(t, func(s *VPNSetup, _ *fakeDevice) { s.Auth = auth })

		_, body := f.get(t)
		if got, ok := body["check"].(map[string]any)["address"]; ok {
			t.Errorf("address = %v while authentication is switched on but NOT being enforced", got)
		}
	})
}

// TestEveryTunnelStateIsA200. The state is the answer. A 503 for a tunnel that
// is down makes the expected failure look identical to a broken curator in a
// browser's network tab, which is the rule every probe in this package follows.
func TestEveryTunnelStateIsA200(t *testing.T) {
	for name, mutate := range map[string]func(*VPNSetup, *fakeDevice){
		"healthy":            nil,
		"never handshaken":   func(_ *VPNSetup, d *fakeDevice) { d.status.LastHandshake = time.Time{} },
		"device unreadable":  func(_ *VPNSetup, d *fakeDevice) { d.err = errors.New("device is closed") },
		"exit is the host's": func(_ *VPNSetup, d *fakeDevice) { d.failWith = vpn.ErrSameExit },
		"held": func(s *VPNSetup, _ *fakeDevice) {
			s.Held = func() (bool, string) { return true, "the tunnel went away" }
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newVPNFixture(t, mutate)
			status, body := f.get(t)
			if status != http.StatusOK {
				t.Errorf("status = %d, want 200 — the state IS the answer", status)
			}
			if body["state"] == "" || body["state"] == nil {
				t.Error("no state reported")
			}
		})
	}
}

// TestProtectedIsAnAndOfEveryFactRatherThanARestatementOfOne.
//
// The headline is what somebody reads instead of the four rows below it, so it
// may not be `state == up` dressed up. A tunnel that is up while the engine's
// socket sits outside it is exactly the disagreement worth showing, and a green
// banner drawn off the tunnel alone would hide it.
func TestProtectedIsAnAndOfEveryFactRatherThanARestatementOfOne(t *testing.T) {
	f := newVPNFixture(t, nil)
	if _, body := f.get(t); body["protected"] != true {
		t.Fatalf("a healthy tunnel is not reported protected: %v", body)
	}

	for name, mutate := range map[string]func(*VPNSetup, *fakeDevice){
		"the socket is not inside the tunnel": func(s *VPNSetup, _ *fakeDevice) {
			s.Binding = func() net.Addr { return tunnelAddr{"192.168.1.26"} }
		},
		"there is no socket at all": func(s *VPNSetup, _ *fakeDevice) {
			s.Binding = func() net.Addr { return nil }
		},
		"downloads are held": func(s *VPNSetup, _ *fakeDevice) {
			s.Held = func() (bool, string) { return true, "the tunnel went away" }
		},
		"the exit is the host's own": func(_ *VPNSetup, d *fakeDevice) { d.failWith = vpn.ErrSameExit },
		"the handshake never happened": func(_ *VPNSetup, d *fakeDevice) {
			d.status.LastHandshake = time.Time{}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newVPNFixture(t, mutate)
			_, body := f.get(t)
			if body["protected"] == true {
				t.Errorf("protected is true with %s: %v", name, body)
			}
		})
	}
}

// TestTheEngineSocketIsReportedAlongsideWhatTheWiringIntended.
//
// Both, because a disagreement between them is the interesting case: `tunnelled`
// is what cmd/curator meant to build and `inside_tunnel` is what the socket
// actually is. Reporting only one hides the case where they differ, which is the
// only case worth a screen.
func TestTheEngineSocketIsReportedAlongsideWhatTheWiringIntended(t *testing.T) {
	f := newVPNFixture(t, nil)
	_, body := f.get(t)

	engine := body["engine"].(map[string]any)
	if engine["socket"] != "10.5.0.2:51820" {
		t.Errorf("socket = %v, want the address read off the engine", engine["socket"])
	}
	if engine["tunnelled"] != true {
		t.Error("tunnelled = false for an engine built with a tunnel")
	}
	if engine["backend"] != "embedded" {
		t.Errorf("backend = %v, want embedded — the scope of the guarantee (D27)", engine["backend"])
	}
}

// TestAProcessWithNoEngineSaysARestartIsNeeded.
//
// The honest limit of making VPN_REQUIRED immediate: the toggle applies to the
// check and cannot conjure an engine. A curator that booted with no tunnel and
// enforcement on has no engine at all, so turning enforcement off still needs a
// restart — and the screen has to say that rather than leave somebody clicking a
// switch that does nothing.
func TestAProcessWithNoEngineSaysARestartIsNeeded(t *testing.T) {
	f := newVPNFixture(t, func(s *VPNSetup, _ *fakeDevice) {
		s.Binding = nil
		s.Held = nil
		s.Tunnelled = false
	})

	_, body := f.get(t)
	enforcement := body["enforcement"].(map[string]any)
	if enforcement["engine_started"] != false {
		t.Errorf("engine_started = %v with no engine in the process", enforcement["engine_started"])
	}
	if enforcement["required"] != true {
		t.Error("required = false; enforcement is what stopped the engine being built")
	}
}

// TestTheForcedCheckIsRateLimited. It makes two round trips to a third party,
// one of them through the tunnel, and it is on a button somebody presses
// repeatedly while watching a tunnel come up.
func TestTheForcedCheckIsRateLimited(t *testing.T) {
	f := newVPNFixture(t, nil)
	before := f.device.checks

	status, _ := f.do(t, http.MethodPost, "/api/vpn/check")
	if status != http.StatusOK {
		t.Fatalf("first check: status = %d, want 200", status)
	}
	if f.device.checks != before+1 {
		t.Fatalf("the forced check did not re-prove: %d checks", f.device.checks-before)
	}

	status, body := f.do(t, http.MethodPost, "/api/vpn/check")
	if status != http.StatusTooManyRequests {
		t.Errorf("second check: status = %d, want 429", status)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "try again in") {
		t.Errorf("the refusal does not say how long to wait: %q", msg)
	}
	if f.device.checks != before+1 {
		t.Error("the rate-limited check ran anyway")
	}
}

// TestAFailedCheckIsStillA200. A tunnel that is down is what this endpoint is
// FOR; reporting it as an error makes the failure path a network-tab mystery
// instead of a sentence on screen.
func TestAFailedCheckIsStillA200(t *testing.T) {
	f := newVPNFixture(t, func(_ *VPNSetup, d *fakeDevice) { d.failWith = vpn.ErrSameExit })

	status, body := f.do(t, http.MethodPost, "/api/vpn/check")
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200", status)
	}
	if body["protected"] == true {
		t.Error("protected is true for a tunnel leaving from the host's own address")
	}
	if detail, _ := body["detail"].(string); detail == "" {
		t.Error("no detail; the sentence is the whole point of a 200 here")
	}
}

// TestAProcessWithNoVPNWiringIsThe503. The one case that is not a state.
func TestAProcessWithNoVPNWiringIsThe503(t *testing.T) {
	srv := New(nil, nil, nil, t.TempDir(), quietVPN())
	mux := http.NewServeMux()
	srv.RegisterVPN(mux)

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/vpn"},
		{http.MethodPost, "/api/vpn/check"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(route.method, route.path, nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s: status = %d, want 503", route.method, route.path, rec.Code)
		}
	}
}

// TestACachedVerdictSaysSo. Without this a page polling every three seconds
// draws a ten-minute-old proof as though it had just been established.
func TestACachedVerdictSaysSo(t *testing.T) {
	f := newVPNFixture(t, nil)

	_, body := f.get(t)
	check := body["check"].(map[string]any)
	if check["checked"] != true {
		t.Error("checked = false after the boot proof")
	}
	if check["fresh"] != true {
		t.Error("fresh = false for the verdict the boot proof just reached")
	}

	// A second GET reads the same stored verdict without re-proving anything,
	// and the freshness travels with it rather than being recomputed.
	if _, second := f.get(t); second["check"].(map[string]any)["different"] != true {
		t.Error("a second read lost what was proved")
	}
}
