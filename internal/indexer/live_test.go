package indexer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

// The two live indexer tests — TestTPBLive and TestYTSLiveSearchInterstellar —
// are the only tests in this repo that reach the public internet without a
// credential to skip on, so they run on every CI runner. Both of them have to
// answer the same question when a search fails: is this network unable to use
// the indexer, or is the indexer broken? This file holds that answer once.
//
// It is one file because the two tests had one half of the rule each, and each
// was red in CI for the half it was missing:
//
//   - T73 gave TPB the STATUS half. It reads a HEAD probe's status and skips on
//     403/401/429, which is what a GitHub Actions runner gets from apibay. But
//     T75 found the verdict was one call too early: the probe can answer 200 and
//     the search that follows can still time out, and TestTPBLive then called
//     t.Fatalf on that transport error. It turned the gate red at tpb_test.go:546
//     with "context deadline exceeded (Client.Timeout exceeded while awaiting
//     headers)" and passed on the next attempt.
//   - yts_test.go gave YTS the TRANSPORT half, and argued it correctly: "only a
//     transport failure is 'no network'." But it never read a status, so a 403
//     from movies-api.accel.li would have failed CI exactly as apibay's did,
//     with T73's fix sitting one file away and not applicable.
//
// So the rule is applied to THE CALL rather than to a probe, and both tests ask
// the same function.
//
// T77 then closed the hole T76 measured and left open: a base URL that has gone
// NXDOMAIN is a *net.DNSError, which is a net.Error, so it took the transport
// branch and SKIPPED — D12's own failure going green under the guard D12 paid
// for. It now fails loudly, but only once a control name has proved that DNS
// works on this machine, because on darwin an offline laptop produces the same
// error value as a dead host. See D42.

// refusedTheNetwork reports whether a status says THIS NETWORK may not use the
// indexer, as opposed to the indexer being broken.
//
// The distinction is the one yts_test.go argues for and it must not be lost: "a
// status YTS should not be returning is a real regression and must not be
// skipped past — that is how a dead base URL stays green for a week." D12 is
// that lesson paid for once already, when yts.mx went NXDOMAIN.
//
// These three are not that. They are an access decision about the caller, and
// they say nothing about whether curator can parse what the indexer returns.
// Every other non-200 still fails the test.
func refusedTheNetwork(status int) bool {
	switch status {
	case http.StatusForbidden, http.StatusUnauthorized, http.StatusTooManyRequests:
		return true
	}
	return false
}

// liveVerdict is what a live indexer test does about a search that failed.
type liveVerdict int

const (
	// liveFail is the default, and it is the default on purpose: a failure only
	// becomes a skip by matching a named rule below.
	liveFail liveVerdict = iota
	liveSkip
)

func (v liveVerdict) String() string {
	if v == liveSkip {
		return "skip"
	}
	return "fail"
}

// classifyLiveFailure decides what a failed live search means, given the error,
// the status of the last response the client actually received (0 when no
// response arrived at all), and a way to ask whether DNS works on this machine.
// It returns the verdict and the reason to print.
//
// It is a pure function taking a status and a func rather than a *http.Response
// and a resolver so that every branch can be asserted directly — a live test
// cannot assert its own skip, because it takes whichever branch the network
// gives it.
//
// dnsWorks is a func, and it is called on exactly one branch: a name that did
// not resolve. Nothing else needs it, and a lookup nobody reads is a second
// network call on every skip.
func classifyLiveFailure(err error, status int, dnsWorks func() bool) (liveVerdict, string) {
	if err == nil {
		return liveFail, "no error"
	}

	// The status half (T73). Checked first because it is the specific case: a
	// refused caller is a successful HTTP transaction, so it would otherwise
	// fall through to the transport check and be misread as a working network.
	if refusedTheNetwork(status) {
		return liveSkip, "the indexer answered " + http.StatusText(status) + " to this network, which is a decision about the caller rather than a broken indexer"
	}

	// The name half (T77, D42). This must be asked BEFORE the transport check
	// below, because a *net.DNSError IS a net.Error: until T77 an NXDOMAIN base
	// URL fell into "no network" and skipped, so D12's own failure would have
	// gone green under the guard D12 paid for.
	//
	// IsNotFound is the NXDOMAIN discriminator and nothing else is: measured
	// 2026-08-18, a resolver that cannot be reached at all gives IsNotFound
	// false and IsTemporary true, and a connection refused is not a
	// *net.DNSError in the first place.
	//
	// It is not trusted on its own, because on darwin it cannot be. Go maps
	// both EAI_NONAME and EAI_NODATA to "no such host" (net/cgo_unix.go:189),
	// and macOS's getaddrinfo answers that way with no network at all — so on a
	// laptop with the WiFi off, "this host is gone" and "this machine is
	// offline" are the same error value. The control name is what separates
	// them, and it is why this is not a one-line IsNotFound check.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		if dnsWorks() {
			return liveFail, "the base URL does not resolve, and DNS works on this machine — the host is gone, which is exactly D12 happening again"
		}
		return liveSkip, "the base URL does not resolve, and neither does the control name, so this machine has no DNS rather than the indexer being gone"
	}

	// The transport half (yts_test.go). Nothing was answered, so there is
	// nothing to regress against.
	var netErr net.Error
	var urlErr *url.Error
	if errors.As(err, &netErr) || errors.As(err, &urlErr) {
		return liveSkip, "the indexer could not be reached at all"
	}

	// Everything else — a status the indexer should not be returning, a decode
	// failure, an API-level error — is a real regression and stays loud.
	return liveFail, "the indexer answered, and answered wrongly"
}

// The control name each live test uses to prove DNS works before it calls its
// own base URL dead. It is the OTHER indexer's host, which makes the two live
// tests each other's control and adds no third party to the suite.
//
// Two properties earned it, both measured 2026-08-18:
//
//   - It still resolves when it refuses. apibay answers 403 to a GitHub address
//     range (T73) and its name resolves from that same runner, so the control
//     survives the one case CI actually hits.
//   - A single dead host is always caught by the test that is not it. If
//     apibay.org goes NXDOMAIN, TestTPBLive asks movies-api.accel.li, gets an
//     answer, and fails loudly. Only both hosts dying in the same run hides
//     either, and that is indistinguishable from an offline machine anyway.
// With three live tests the arrangement is a CYCLE rather than a pair, and it
// keeps both properties above: every host is somebody's control, no host is its
// own, and no third party is introduced. TestEachIndexerIsTheOthersControl
// pins that it stays a cycle.
const (
	tpbControlHost  = "movies-api.accel.li" // YTS's host guards TPB's
	ytsControlHost  = "eztvx.to"            // EZTV's guards YTS's
	eztvControlHost = "apibay.org"          // and TPB's guards EZTV's
)

// liveDNSWorks returns the dnsWorks func classifyLiveFailure asks on the one
// branch that needs it: does the control name resolve from here?
//
// A lookup error of any kind counts as "no DNS", which is the safe direction —
// it skips rather than failing a build on a resolver that is merely unwell.
func liveDNSWorks(ctx context.Context, control string) func() bool {
	return func() bool {
		lookupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		addrs, err := net.DefaultResolver.LookupHost(lookupCtx, control)
		return err == nil && len(addrs) > 0
	}
}

// liveRecorder remembers the status of the last response its transport saw, so a
// live test can classify a failure by what the server actually answered rather
// than by matching the text of an error string.
//
// The indexers format a refused status into a plain fmt.Errorf — apibay's is
// `tpb search %q: apibay returned %s` — so the status is not recoverable with
// errors.As. Recording it in the transport gets it without changing a production
// error type for a test's benefit.
type liveRecorder struct {
	mu     sync.Mutex
	next   http.RoundTripper
	status int
}

// RoundTrip implements http.RoundTripper.
func (r *liveRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.next.RoundTrip(req)
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		// No response arrived, so there is no status to attribute to this call.
		r.status = 0
		return resp, err
	}
	r.status = resp.StatusCode
	return resp, err
}

// lastStatus is the status of the last response received, or 0 if the last
// request never got one.
func (r *liveRecorder) lastStatus() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

// liveClient builds the http.Client a live indexer test should use, together
// with the recorder that reads its statuses back.
func liveClient(timeout time.Duration) (*http.Client, *liveRecorder) {
	rec := &liveRecorder{next: http.DefaultTransport}
	return &http.Client{Timeout: timeout, Transport: rec}, rec
}

// The classifier is the whole fix, so it is the thing with a test.
//
// A live test cannot assert its own skip — it takes whichever branch the network
// gives it — so what is pinned here is the decision it makes, every way round.
func TestARefusedNetworkIsNotABrokenIndexer(t *testing.T) {
	for _, status := range []int{
		http.StatusForbidden,       // what a GitHub Actions runner gets from apibay
		http.StatusUnauthorized,    // the same decision, differently worded
		http.StatusTooManyRequests, // a rate limit is about the caller too
	} {
		if !refusedTheNetwork(status) {
			t.Errorf("status %d must count as a refused network, or CI cannot pass", status)
		}
	}
	for _, status := range []int{
		http.StatusOK,
		http.StatusNotFound,            // the endpoint moved: a real regression
		http.StatusInternalServerError, // the indexer is broken, and must be loud
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
	} {
		if refusedTheNetwork(status) {
			t.Errorf("status %d must NOT be skipped past — that is how a dead base URL stays green for a week", status)
		}
	}
}

// dnsUp and dnsDown stand in for the control lookup. The point of injecting it
// is that both answers are assertable: the live tests themselves can only ever
// take the branch the machine they run on happens to give them.
func dnsUp() bool   { return true }
func dnsDown() bool { return false }

func TestClassifyLiveFailureCoversTheCallAndNotJustTheProbe(t *testing.T) {
	// The errors below carry the shapes each indexer actually produces:
	// client.Do's *url.Error, wrapped with %w the way both Search methods do.

	// T75's case, and it is a TIMEOUT rather than a name failure — the T76
	// version of this table called a *net.DNSError "timedOut" and asserted a
	// skip, which is the same conflation T77 exists to undo.
	timedOut := fmt.Errorf("tpb search %q: %w", "Interstellar", &url.Error{
		Op:  "Get",
		URL: "https://apibay.org/q.php",
		Err: context.DeadlineExceeded,
	})

	// A base URL that is GONE. Measured 2026-08-18 against the real yts.mx:
	// *net.DNSError{Err:"no such host", IsNotFound:true}, IsTimeout false.
	nxdomain := fmt.Errorf("yts search %q: %w", "Interstellar", &url.Error{
		Op:  "Get",
		URL: "https://yts.mx/api/v2/list_movies.json",
		Err: &net.DNSError{Err: "no such host", Name: "yts.mx", IsNotFound: true},
	})

	// An OFFLINE machine, pure-Go resolver. Measured the same day: the resolver
	// itself could not be dialled, so IsNotFound is FALSE and IsTemporary true.
	// This is the shape that must never be read as a dead host.
	offline := fmt.Errorf("yts search %q: %w", "Interstellar", &url.Error{
		Op:  "Get",
		URL: "https://movies-api.accel.li/api/v2/list_movies.json",
		Err: &net.DNSError{
			Err:         "dial udp: network is unreachable",
			Name:        "movies-api.accel.li",
			Server:      "100.64.0.2:53",
			IsTemporary: true,
		},
	})

	refused := errors.New(`tpb search "Interstellar": apibay returned 403 Forbidden`)
	broken := errors.New(`yts search "Interstellar": unexpected status 500 Internal Server Error: `)
	decode := fmt.Errorf("yts search %q: decode response: %w", "Interstellar", errors.New("invalid character"))

	for _, tt := range []struct {
		name   string
		err    error
		status int
		dns    func() bool
		want   liveVerdict
	}{
		// The T75 hole: the probe answered, the call did not.
		{"transport failure after a 200 probe", timedOut, http.StatusOK, dnsUp, liveSkip},
		{"transport failure with no response at all", timedOut, 0, dnsUp, liveSkip},

		// The T73 case, read from the call rather than from a probe.
		{"refused caller", refused, http.StatusForbidden, dnsUp, liveSkip},
		{"unauthorized", refused, http.StatusUnauthorized, dnsUp, liveSkip},
		{"rate limited", refused, http.StatusTooManyRequests, dnsUp, liveSkip},

		// The T74 open question: YTS had no status escape hatch at all.
		{"yts refuses the runner", broken, http.StatusForbidden, dnsUp, liveSkip},

		// D12's rule, and the reason "skip on any non-200" is the wrong fix.
		{"a status the indexer should not return", broken, http.StatusInternalServerError, dnsUp, liveFail},
		{"the endpoint moved", broken, http.StatusNotFound, dnsUp, liveFail},
		{"a decode failure is a real regression", decode, http.StatusOK, dnsUp, liveFail},

		// T77 / D42. THE GAP T76 MEASURED AND LEFT OPEN. Before this, both of
		// the next two skipped, so D12's own failure went green under D12's
		// own guard.
		{"the base URL is gone and DNS works here", nxdomain, 0, dnsUp, liveFail},
		{"a dead name after a 200 from an earlier call", nxdomain, http.StatusOK, dnsUp, liveFail},

		// ...and the promise that is not allowed to break: an offline machine
		// does not fail the build. On darwin these two are the SAME error
		// value, which is why the control name exists.
		{"nothing resolves, including the control", nxdomain, 0, dnsDown, liveSkip},
		{"the resolver itself is unreachable", offline, 0, dnsUp, liveSkip},
		{"the resolver is unreachable and so is the control", offline, 0, dnsDown, liveSkip},

		// Precedence: a refused status is a successful transaction, so it is
		// answered before the name is ever questioned.
		{"a refusal outranks a name failure", nxdomain, http.StatusForbidden, dnsDown, liveSkip},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, why := classifyLiveFailure(tt.err, tt.status, tt.dns)
			if got != tt.want {
				t.Errorf("verdict = %s, want %s (reason given: %s)", got, tt.want, why)
			}
			if why == "" {
				t.Error("every verdict must carry a reason: it is what the skip line prints")
			}
		})
	}
}

// The control lookup is a network call, so it may only happen on the one branch
// that reads it. Every other verdict is reachable with no DNS at all, and a skip
// that quietly resolves a name is a second way for a live test to be slow.
func TestTheControlNameIsOnlyLookedUpForANameFailure(t *testing.T) {
	nxdomain := &url.Error{Op: "Get", URL: "https://yts.mx/",
		Err: &net.DNSError{Err: "no such host", Name: "yts.mx", IsNotFound: true}}

	for _, tt := range []struct {
		name   string
		err    error
		status int
		want   bool
	}{
		{"a name failure asks", nxdomain, 0, true},
		{"a refused caller does not", errors.New("403"), http.StatusForbidden, false},
		{"a wrong status does not", errors.New("500"), http.StatusInternalServerError, false},
		{"a timeout does not", &url.Error{Err: context.DeadlineExceeded}, 0, false},
		{"a nil error does not", nil, 0, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var asked bool
			classifyLiveFailure(tt.err, tt.status, func() bool { asked = true; return true })
			if asked != tt.want {
				t.Errorf("control name looked up = %v, want %v", asked, tt.want)
			}
		})
	}
}

// The control is ANOTHER indexer's host, and that is the whole reason no third
// party enters the suite. Two properties have to hold, and with three live
// tests neither is obvious by inspection any more:
//
//   - No indexer is its own control. Guarding a host with itself proves nothing
//     — the lookup that failed is the lookup being asked again.
//   - Every control is a host this suite actually searches, so it is a name
//     somebody would notice going away.
//
// Adding a fourth live indexer means adding it to this table. A control left
// pointing at a retired host is the failure this test exists to make loud.
func TestEachIndexerIsTheOthersControl(t *testing.T) {
	for _, tt := range []struct {
		indexer string
		base    string // the base URL this indexer searches
		control string // the host that guards it
	}{
		{"tpb", tpbSite, tpbControlHost},
		{"yts", DefaultYTSBaseURL, ytsControlHost},
		{"eztv", DefaultEZTVBaseURL, eztvControlHost},
	} {
		if strings.Contains(tt.base, tt.control) {
			t.Errorf("%s is its own control (%q): a host cannot guard itself", tt.indexer, tt.control)
		}
		// The control must be some other indexer's real base URL. Anything else
		// is a third party, which is what this arrangement exists to avoid.
		var known bool
		for _, base := range []string{tpbSite, DefaultYTSBaseURL, DefaultEZTVBaseURL} {
			if strings.Contains(base, tt.control) {
				known = true
			}
		}
		if !known {
			t.Errorf("%s's control is %q, which is not a host this suite searches", tt.indexer, tt.control)
		}
	}
}

// The recorder is what makes the status half reach the call, so its one
// non-obvious property gets pinned: a request that never gets a response must
// not leave the previous call's status behind for the classifier to read.
func TestLiveRecorderForgetsTheStatusWhenNothingAnswered(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	client, rec := liveClient(5 * time.Second)
	if got := rec.lastStatus(); got != 0 {
		t.Errorf("before any request, lastStatus = %d, want 0", got)
	}

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if got := rec.lastStatus(); got != http.StatusForbidden {
		t.Errorf("lastStatus = %d, want %d", got, http.StatusForbidden)
	}

	// Now a request that cannot connect. If the 403 above survived it, the
	// classifier would skip a genuine transport failure for the wrong reason.
	if _, err := client.Get("http://127.0.0.1:1/"); err == nil {
		t.Fatal("want an error from a port nothing listens on")
	}
	if got := rec.lastStatus(); got != 0 {
		t.Errorf("after a failed request, lastStatus = %d, want 0", got)
	}
}
