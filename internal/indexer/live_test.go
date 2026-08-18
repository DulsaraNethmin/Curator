package indexer

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// classifyLiveFailure decides what a failed live search means, given the error
// and the status of the last response the client actually received (0 when no
// response arrived at all). It returns the verdict and the reason to print.
//
// It is a pure function taking a status rather than a *http.Response so that
// every branch can be asserted directly — a live test cannot assert its own
// skip, because it takes whichever branch the network gives it.
//
// KNOWN GAP, measured 2026-08-18 and deliberately left open: a base URL that has
// gone NXDOMAIN takes the transport branch and SKIPS. `yts.mx` still does not
// resolve, and a search against it fails with `*net.DNSError{IsNotFound:true}`,
// which is a net.Error, so the rule above calls it "no network". D12's own
// failure would therefore not fail loudly today, and neither test's comment says
// so. NXDOMAIN is distinguishable — connection-refused is not a *net.DNSError at
// all — but separating "this host is gone" from "this machine is offline" is a
// decision about whose build goes red, not a mechanical fix, and it is not this
// task's. See docs/tasks/T76-a-skip-that-covers-the-call.md.
func classifyLiveFailure(err error, status int) (liveVerdict, string) {
	if err == nil {
		return liveFail, "no error"
	}

	// The status half (T73). Checked first because it is the specific case: a
	// refused caller is a successful HTTP transaction, so it would otherwise
	// fall through to the transport check and be misread as a working network.
	if refusedTheNetwork(status) {
		return liveSkip, "the indexer answered " + http.StatusText(status) + " to this network, which is a decision about the caller rather than a broken indexer"
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

func TestClassifyLiveFailureCoversTheCallAndNotJustTheProbe(t *testing.T) {
	// A transport error carrying the shape each indexer actually produces:
	// client.Do's *url.Error, wrapped with %w the way both SearchMovie do.
	timedOut := fmt.Errorf("tpb search %q: %w", "Interstellar", &url.Error{
		Op:  "Get",
		URL: "https://apibay.org/q.php",
		Err: &net.DNSError{Err: "no such host", IsNotFound: true},
	})
	refused := errors.New(`tpb search "Interstellar": apibay returned 403 Forbidden`)
	broken := errors.New(`yts search "Interstellar": unexpected status 500 Internal Server Error: `)
	decode := fmt.Errorf("yts search %q: decode response: %w", "Interstellar", errors.New("invalid character"))

	for _, tt := range []struct {
		name   string
		err    error
		status int
		want   liveVerdict
	}{
		// The T75 hole: the probe answered, the call did not. This is the case
		// that turned the gate red and t.Fatalf'd on a timeout.
		{"transport failure after a 200 probe", timedOut, http.StatusOK, liveSkip},
		{"transport failure with no response at all", timedOut, 0, liveSkip},

		// The T73 case, now read from the call rather than from a probe.
		{"refused caller", refused, http.StatusForbidden, liveSkip},
		{"unauthorized", refused, http.StatusUnauthorized, liveSkip},
		{"rate limited", refused, http.StatusTooManyRequests, liveSkip},

		// The T74 open question: YTS had no status escape hatch at all.
		{"yts refuses the runner", broken, http.StatusForbidden, liveSkip},

		// D12's rule, and the reason "skip on any non-200" is the wrong fix.
		{"a status the indexer should not return", broken, http.StatusInternalServerError, liveFail},
		{"the endpoint moved", broken, http.StatusNotFound, liveFail},
		{"a decode failure is a real regression", decode, http.StatusOK, liveFail},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, why := classifyLiveFailure(tt.err, tt.status)
			if got != tt.want {
				t.Errorf("verdict = %s, want %s (reason given: %s)", got, tt.want, why)
			}
			if why == "" {
				t.Error("every verdict must carry a reason: it is what the skip line prints")
			}
		})
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
