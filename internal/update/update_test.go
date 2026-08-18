package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTenIsNewerThanNineEvenThoughItSortsBefore(t *testing.T) {
	// The reason Newer parses instead of comparing strings. As text "0.10.0" <
	// "0.9.0", so a string compare would stop offering updates at the tenth
	// release of a 0.x line and nobody would notice until much later.
	if !Newer("0.9.0", "0.10.0") {
		t.Fatal("0.10.0 was not newer than 0.9.0")
	}
	if Newer("0.10.0", "0.9.0") {
		t.Fatal("0.9.0 was reported newer than 0.10.0")
	}
}

func TestNewerIsFalseForTheSameVersionAndForOlderOnes(t *testing.T) {
	for _, c := range []struct{ cur, lat string }{
		{"0.1.0", "0.1.0"},
		{"1.2.3", "1.2.2"},
		{"1.2.3", "0.9.9"},
	} {
		if Newer(c.cur, c.lat) {
			t.Errorf("Newer(%q, %q) was true", c.cur, c.lat)
		}
	}
}

func TestTheGitTagAndTheImageTagCompareEqual(t *testing.T) {
	// release.yml strips the v, so the running binary says 0.1.0 while the
	// release feed says v0.1.0. Without Normalise every install believes it is
	// behind itself.
	if Newer("0.1.0", "v0.1.0") {
		t.Fatal("v0.1.0 was reported newer than the 0.1.0 it names")
	}
}

func TestAnUnparseableVersionNeverOffersAnUpdate(t *testing.T) {
	// A phantom update prompt is worse than a missed one: it asks somebody to
	// restart their server for nothing.
	for _, c := range []struct{ cur, lat string }{
		{"0.1.0", "nightly"},
		{"", "0.2.0"},
		{"0.1.0", ""},
		{"0.1.0", "1.2.3.4"},
	} {
		if Newer(c.cur, c.lat) {
			t.Errorf("Newer(%q, %q) offered an update", c.cur, c.lat)
		}
	}
}

func TestAPrereleaseSuffixDoesNotBreakTheComparison(t *testing.T) {
	if !Newer("0.1.0", "0.2.0-rc.1") {
		t.Fatal("0.2.0-rc.1 was not newer than 0.1.0")
	}
}

func feed(t *testing.T, status int, body string) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestANewerReleaseIsReportedWithItsNotes(t *testing.T) {
	srv, _ := feed(t, 200, `{"tag_name":"v0.2.0","html_url":"https://example.test/r","body":"fixed things"}`)
	got := NewChecker("0.1.0", srv.URL, srv.Client()).Check(context.Background(), false)

	if !got.Available || got.Latest != "0.2.0" {
		t.Fatalf("available=%v latest=%q", got.Available, got.Latest)
	}
	if got.Notes != "fixed things" || got.URL != "https://example.test/r" {
		t.Fatalf("notes=%q url=%q", got.Notes, got.URL)
	}
	if got.Error != "" {
		t.Fatalf("unexpected error: %s", got.Error)
	}
}

func TestTheFeedIsAskedOnceUntilForced(t *testing.T) {
	// GitHub allows 60 unauthenticated requests an hour per address and a
	// household may run more than one curator behind it, so the screen loading
	// repeatedly must not each cost a request.
	srv, calls := feed(t, 200, `{"tag_name":"v0.2.0"}`)
	c := NewChecker("0.1.0", srv.URL, srv.Client())

	c.Check(context.Background(), false)
	c.Check(context.Background(), false)
	c.Check(context.Background(), false)
	if *calls != 1 {
		t.Fatalf("the feed was asked %d times, want 1", *calls)
	}

	c.Check(context.Background(), true)
	if *calls != 2 {
		t.Fatalf("force did not re-ask: %d calls", *calls)
	}
}

func TestAFailedCheckKeepsTheAnswerItAlreadyHad(t *testing.T) {
	// Blanking the version on a transient failure reads as "you are up to
	// date", which is the one wrong answer this screen must never give.
	var status = 200
	var body = `{"tag_name":"v0.2.0","html_url":"https://example.test/r"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	c := NewChecker("0.1.0", srv.URL, srv.Client())
	if first := c.Check(context.Background(), false); !first.Available {
		t.Fatal("the first check did not see the release")
	}

	status, body = 500, ``
	got := c.Check(context.Background(), true)
	if got.Latest != "0.2.0" || !got.Available {
		t.Fatalf("the known release was lost: latest=%q available=%v", got.Latest, got.Available)
	}
	if got.Error == "" {
		t.Fatal("the failure was not reported alongside it")
	}
}

func TestTheRateLimitSaysSoRatherThanReportingAPermissionProblem(t *testing.T) {
	srv, _ := feed(t, 403, `{"message":"API rate limit exceeded"}`)
	got := NewChecker("0.1.0", srv.URL, srv.Client()).Check(context.Background(), false)
	if got.Error == "" {
		t.Fatal("no error reported")
	}
	if !contains(got.Error, "rate limit") {
		t.Fatalf("error did not name the rate limit: %q", got.Error)
	}
}

func TestADraftReleaseIsNotAnUpdate(t *testing.T) {
	srv, _ := feed(t, 200, `{"tag_name":"v9.9.9","draft":true}`)
	got := NewChecker("0.1.0", srv.URL, srv.Client()).Check(context.Background(), false)
	if got.Available {
		t.Fatal("a draft release was offered as an update")
	}
}

func TestNoUpdaterIsASentinelRatherThanAFailure(t *testing.T) {
	// The screen shows a command to paste instead of a button, which is the
	// correct outcome for every install without watchtower beside it.
	var u *Updater = NewUpdater("", "", nil)
	if u.Configured() {
		t.Fatal("an empty URL produced a configured updater")
	}
	if err := u.Trigger(context.Background()); err != ErrNoUpdater {
		t.Fatalf("got %v, want ErrNoUpdater", err)
	}
}

func TestTriggerSendsTheTokenAndAcceptsWatchtowersAnswer(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		w.WriteHeader(200)
	}))
	defer srv.Close()

	if err := NewUpdater(srv.URL+"/", "s3cret", srv.Client()).Trigger(context.Background()); err != nil {
		t.Fatalf("trigger: %v", err)
	}
	if gotPath != "/v1/update" {
		t.Fatalf("path %q, want /v1/update", gotPath)
	}
	if gotAuth != "Bearer s3cret" {
		t.Fatalf("auth %q", gotAuth)
	}
}

func TestARefusedTokenIsNamedRatherThanReportedAsUnreachable(t *testing.T) {
	// Same symptom, different fix: a wrong address and a wrong token both
	// produce "it did not work" without this.
	srv, _ := feed(t, 401, ``)
	err := NewUpdater(srv.URL, "wrong", srv.Client()).Trigger(context.Background())
	if err == nil || !contains(err.Error(), "token") {
		t.Fatalf("got %v, want an error naming the token", err)
	}
}

func TestProbingIsRefusedBecauseTheUpdateEndpointIsAGet(t *testing.T) {
	// Regression guard for a flaw found by running the thing: watchtower's
	// /v1/update is a GET, so a "is it reachable" probe RESTARTS the server.
	// An Updater must therefore never contact the updater except to trigger.
	var contacted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contacted = true
		w.WriteHeader(200)
	}))
	defer srv.Close()

	u := NewUpdater(srv.URL, "s3cret", srv.Client())
	if !u.Configured() {
		t.Fatal("a configured updater reported itself unconfigured")
	}
	if contacted {
		t.Fatal("asking whether the updater is configured contacted it, which would restart curator")
	}
}

func TestCheckedAtIsAlwaysStamped(t *testing.T) {
	srv, _ := feed(t, 500, ``)
	got := NewChecker("0.1.0", srv.URL, srv.Client()).Check(context.Background(), false)
	if got.CheckedAt.IsZero() || time.Since(got.CheckedAt) > time.Minute {
		t.Fatalf("CheckedAt = %v", got.CheckedAt)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func TestNoReleasesYetIsSaidPlainlyRatherThanAsAFault(t *testing.T) {
	// The state this repository was actually in when the check was written: a
	// git tag, a published image, and no release object at all, so the feed
	// answered 404. "Something is broken" would be the wrong sentence for a
	// perfectly healthy install.
	srv, _ := feed(t, 404, `{"message":"Not Found"}`)
	got := NewChecker("0.1.0", srv.URL, srv.Client()).Check(context.Background(), false)

	if got.Available {
		t.Fatal("a 404 offered an update")
	}
	if !contains(got.Error, "no releases") {
		t.Fatalf("error was %q, want it to say no releases have been published", got.Error)
	}
}
