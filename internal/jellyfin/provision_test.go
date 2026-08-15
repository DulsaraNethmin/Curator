package jellyfin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- a fake Jellyfin that answers exactly what 10.10.7 answered --------------

// recorded is one request the fake received. The request log is the assertion
// that matters most in this file: the guard's whole job is that a call is NOT
// made, and an error value alone cannot tell the difference between a refusal
// and a server that happened to say no.
type recorded struct {
	Method string
	Path   string
	Query  url.Values
	Body   []byte
	Header http.Header
}

func (r recorded) route() string { return r.Method + " " + r.Path }

// fakeJellyfin answers the measured sequence and remembers what it was asked.
//
// It is deliberately stateful rather than a table of canned replies: the
// properties under test are things like "the key came from the listing and not
// from the POST" and "a second provision does not mint a second key", and
// neither is observable against a server that cannot remember anything.
type fakeJellyfin struct {
	http *httptest.Server

	mu              sync.Mutex
	got             []recorded
	wizardCompleted bool
	keys            []map[string]any
	folders         []map[string]any
	minted          int
	// paths is what exists on this server's own filesystem, which is a
	// different question from what its libraries point at — and the difference
	// is exactly T66's third case.
	paths map[string]bool
	// override replaces the answer for one "METHOD /path", which is how the
	// degrade paths are tested without a second fake.
	override map[string]http.HandlerFunc
}

func newFakeJellyfin(t *testing.T) *fakeJellyfin {
	t.Helper()
	// "/" exists on every filesystem, which is the fact PathExists leans on to
	// tell a missing path from a missing endpoint.
	f := &fakeJellyfin{override: map[string]http.HandlerFunc{}, paths: map[string]bool{"/": true}}
	f.http = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.http.Close)
	return f
}

func (f *fakeJellyfin) provisioner() *Provisioner {
	return NewProvisioner(f.http.URL, "0.1.0", f.http.Client(), discardLogger())
}

func (f *fakeJellyfin) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	f.mu.Lock()
	f.got = append(f.got, recorded{
		Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(),
		Body: body, Header: r.Header.Clone(),
	})
	route := r.Method + " " + r.URL.Path
	override := f.override[route]
	f.mu.Unlock()

	if override != nil {
		override(w, r)
		return
	}

	switch route {
	case "GET " + pathSystemInfoPublic:
		f.mu.Lock()
		done := f.wizardCompleted
		f.mu.Unlock()
		writeJSON(w, map[string]any{
			"Version": "10.10.7", "Id": "srv-1", "StartupWizardCompleted": done,
		})

	case "GET " + pathStartupConfiguration:
		writeJSON(w, map[string]any{
			"UICulture": "en-US", "MetadataCountryCode": "US", "PreferredMetadataLanguage": "en",
		})
	case "POST " + pathStartupConfiguration:
		w.WriteHeader(http.StatusNoContent)

	case "GET " + pathStartupUser:
		writeJSON(w, map[string]any{"Name": "root"})
	case "POST " + pathStartupUser:
		w.WriteHeader(http.StatusNoContent)

	case "POST " + pathStartupRemoteAccess:
		w.WriteHeader(http.StatusNoContent)

	case "POST " + pathStartupComplete:
		f.mu.Lock()
		f.wizardCompleted = true
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)

	case "POST " + pathAuthenticateByName:
		// Measured: without the Authorization header this is 400, not 401.
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{
			"AccessToken": "0123456789abcdef0123456789abcdef",
			"ServerId":    "srv-1",
			"User":        map[string]any{"Id": "user-1"},
		})

	case "POST " + pathVirtualFolders:
		if r.Header.Get(tokenHeader) == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var sent struct {
			LibraryOptions struct {
				PathInfos []struct {
					Path string `json:"Path"`
				} `json:"PathInfos"`
			} `json:"LibraryOptions"`
		}
		_ = json.Unmarshal(body, &sent)
		locations := []any{}
		for _, info := range sent.LibraryOptions.PathInfos {
			locations = append(locations, info.Path)
		}
		f.mu.Lock()
		f.folders = append(f.folders, map[string]any{
			"Name": r.URL.Query().Get("name"), "Locations": locations,
		})
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)

	case "GET " + pathVirtualFolders:
		if r.Header.Get(tokenHeader) == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		folders := f.folders
		f.mu.Unlock()
		if folders == nil {
			folders = []map[string]any{}
		}
		writeJSON(w, folders)

	case "POST " + pathAuthKeys:
		if r.Header.Get(tokenHeader) == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.minted++
		// Measured: the response is 204 with NO body. A provisioner that read
		// the key from here would work against a friendlier fake and fail
		// against Jellyfin.
		f.keys = append(f.keys, map[string]any{
			"AccessToken": fmt.Sprintf("minted-key-%d", f.minted),
			"AppName":     r.URL.Query().Get("app"),
			"DateCreated": "2026-08-15T10:00:00.0000000Z",
			"Id":          0,
		})
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)

	case "POST " + pathValidatePath:
		// Measured on 10.10.7: 204 when the path is there, 404 when it is not,
		// 401 unauthenticated. The 404 body is ASP.NET's stock problem+json —
		// byte-for-byte what a route that did not exist would answer — which is
		// the whole reason PathExists asks about "/" before believing one.
		if r.Header.Get(tokenHeader) == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var sent struct {
			Path             string `json:"Path"`
			ValidateWritable bool   `json:"ValidateWritable"`
		}
		_ = json.Unmarshal(body, &sent)
		if sent.ValidateWritable {
			// Never sent by this package: the writable branch writes a probe
			// file into somebody else's library. Answered as a failure so a
			// change that started sending it fails here rather than on a NAS.
			w.WriteHeader(http.StatusForbidden)
			return
		}
		f.mu.Lock()
		_, present := f.paths[trimPath(sent.Path)]
		f.mu.Unlock()
		if !present {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(
				`{"type":"https://tools.ietf.org/html/rfc9110#section-15.5.5","title":"Not Found","status":404}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case "GET " + pathAuthKeys:
		if r.Header.Get(tokenHeader) == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		keys := f.keys
		f.mu.Unlock()
		if keys == nil {
			keys = []map[string]any{}
		}
		writeJSON(w, map[string]any{"Items": keys})

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (f *fakeJellyfin) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.got...)
}

func (f *fakeJellyfin) routes() []string {
	var routes []string
	for _, r := range f.requests() {
		routes = append(routes, r.route())
	}
	return routes
}

// routesExcept is the sequence with the guard's own reads taken out, so that a
// test about the order of the wizard is not a test about how often the guard
// runs.
func (f *fakeJellyfin) routesExcept(skip string) []string {
	var routes []string
	for _, route := range f.routes() {
		if route != skip {
			routes = append(routes, route)
		}
	}
	return routes
}

func (f *fakeJellyfin) forget() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = nil
}

func (f *fakeJellyfin) complete(done bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.wizardCompleted = done
}

func (f *fakeJellyfin) answer(route string, handler http.HandlerFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.override[route] = handler
}

func (f *fakeJellyfin) seedKeys(keys ...map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, keys...)
}

func (f *fakeJellyfin) mintCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.minted
}

func writeJSON(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// --- the sequence -----------------------------------------------------------

// The whole provision, in the order the measured table gives, against a fake
// that answers exactly those statuses.
//
// The order is asserted rather than only the outcome: every step here works
// against a permissive server in almost any order, and the one that does not
// is the real one.
func TestProvisionRunsTheMeasuredSequence(t *testing.T) {
	fake := newFakeJellyfin(t)
	p := fake.provisioner()
	ctx := context.Background()

	status, err := p.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.Version != "10.10.7" || status.ServerID != "srv-1" || status.WizardCompleted {
		t.Fatalf("status = %+v, want 10.10.7 / srv-1 / not completed", status)
	}

	if err := p.Configure(ctx, StartupConfiguration{}); err != nil {
		t.Fatalf("Configure: %v", err)
	}
	if err := p.CreateAdmin(ctx, "nethmin", "hunter2"); err != nil {
		t.Fatalf("CreateAdmin: %v", err)
	}
	if err := p.EnableRemoteAccess(ctx); err != nil {
		t.Fatalf("EnableRemoteAccess: %v", err)
	}
	session, err := p.Authenticate(ctx, "nethmin", "hunter2")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if session.Token == "" || session.UserID != "user-1" || session.ServerID != "srv-1" {
		t.Fatalf("session = %+v, want a token, user-1 and srv-1", session)
	}
	if err := p.AddLibrary(ctx, session, "Movies", "/media/movies", OnlyIfUnconfigured); err != nil {
		t.Fatalf("AddLibrary: %v", err)
	}
	key, err := p.MintKey(ctx, session, KeyAppName, OnlyIfUnconfigured)
	if err != nil {
		t.Fatalf("MintKey: %v", err)
	}
	// The key must be the one the fake filed, which it never put in a response
	// body to the POST. Anything else means it was invented here.
	if key != "minted-key-1" {
		t.Errorf("key = %q, want the one GET /Auth/Keys listed", key)
	}
	if err := p.CompleteSetup(ctx); err != nil {
		t.Fatalf("CompleteSetup: %v", err)
	}

	// Authenticating, creating the library and minting the key all happen
	// BEFORE the wizard is closed, so a failure leaves the server visibly
	// unfinished rather than finished and wrong.
	want := []string{
		"GET " + pathStartupConfiguration,
		"POST " + pathStartupConfiguration,
		"GET " + pathStartupUser,
		"POST " + pathStartupUser,
		"POST " + pathStartupRemoteAccess,
		"POST " + pathAuthenticateByName,
		"POST " + pathVirtualFolders,
		"GET " + pathVirtualFolders,
		"GET " + pathAuthKeys,
		"POST " + pathAuthKeys,
		"GET " + pathAuthKeys,
		"POST " + pathStartupComplete,
	}
	got := fake.routesExcept("GET " + pathSystemInfoPublic)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("sequence:\n got %v\nwant %v", got, want)
	}

	// And the server really is finished: the fake flipped its own flag on
	// /Startup/Complete, so the guard now refuses.
	if err := p.EnableRemoteAccess(ctx); !errors.Is(err, ErrAlreadyConfigured) {
		t.Errorf("after CompleteSetup: err = %v, want ErrAlreadyConfigured", err)
	}
}

// Every /Startup/ write is preceded by a fresh read of /System/Info/Public.
func TestEveryStartupWriteIsPrecededByAGuardRead(t *testing.T) {
	fake := newFakeJellyfin(t)
	p := fake.provisioner()
	ctx := context.Background()

	for _, step := range []func() error{
		func() error { return p.Configure(ctx, StartupConfiguration{}) },
		func() error { return p.CreateAdmin(ctx, "nethmin", "hunter2") },
		func() error { return p.EnableRemoteAccess(ctx) },
		func() error { return p.CompleteSetup(ctx) },
	} {
		fake.forget()
		fake.complete(false)
		if err := step(); err != nil {
			t.Fatalf("step: %v", err)
		}
		routes := fake.routes()
		if len(routes) == 0 || routes[0] != "GET "+pathSystemInfoPublic {
			t.Errorf("first request was %v, want the guard read", routes)
		}
	}
}

// --- the guard --------------------------------------------------------------

// startupMethods is every method that touches /Startup/, which is exactly the
// set that must refuse a configured server unconditionally.
func startupMethods(p *Provisioner, ctx context.Context) map[string]func() error {
	return map[string]func() error{
		"Configure":          func() error { return p.Configure(ctx, StartupConfiguration{}) },
		"CreateAdmin":        func() error { return p.CreateAdmin(ctx, "attacker", "x") },
		"EnableRemoteAccess": func() error { return p.EnableRemoteAccess(ctx) },
		"CompleteSetup":      func() error { return p.CompleteSetup(ctx) },
	}
}

// The reason this type exists (docs/decisions.md D34).
//
// Measured on a real 10.10.7: POST /Startup/User with a valid API key against a
// server reporting StartupWizardCompleted:true answered 204 and renamed the
// admin account and changed its password. Jellyfin does not protect a
// configured server, so the assertion here is not that curator got an error
// back — it is that the request was never sent at all.
func TestEveryStartupMethodRefusesAConfiguredServerWithoutSendingAnything(t *testing.T) {
	fake := newFakeJellyfin(t)
	fake.complete(true)
	p := fake.provisioner()
	ctx := context.Background()

	for name, call := range startupMethods(p, ctx) {
		fake.forget()

		if err := call(); !errors.Is(err, ErrAlreadyConfigured) {
			t.Errorf("%s: err = %v, want ErrAlreadyConfigured", name, err)
		}
		for _, r := range fake.requests() {
			if strings.HasPrefix(r.Path, startupPrefix) {
				t.Errorf("%s: sent %s to a configured server — the guard is decorative", name, r.route())
			}
		}
	}
}

// The guard reads the server every time and caches nothing, because the failure
// being defended against is a server that changed underneath a half-finished
// flow: the user finishing the wizard in another tab is the ordinary way that
// happens.
func TestTheGuardIsReReadOnEveryCall(t *testing.T) {
	fake := newFakeJellyfin(t)
	// Atomic because the handler runs on the server's goroutine: the round trip
	// happens to order these, and a test that depends on that is a flake
	// waiting for a slower machine.
	var reads atomic.Int64
	fake.answer("GET "+pathSystemInfoPublic, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"Version": "10.10.7", "Id": "srv-1", "StartupWizardCompleted": reads.Add(1) > 1,
		})
	})

	p := fake.provisioner()
	ctx := context.Background()

	if err := p.EnableRemoteAccess(ctx); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := p.EnableRemoteAccess(ctx); !errors.Is(err, ErrAlreadyConfigured) {
		t.Fatalf("second call: err = %v, want ErrAlreadyConfigured — the first answer was cached", err)
	}
	if got := reads.Load(); got != 2 {
		t.Errorf("read /System/Info/Public %d times, want one per call", got)
	}
}

// The two additive operations are the adopt branch (T66), and they need the
// caller to have said so. The zero value refuses.
func TestAddLibraryAndMintKeyNeedAnExplicitOptInOnAConfiguredServer(t *testing.T) {
	fake := newFakeJellyfin(t)
	fake.complete(true)
	p := fake.provisioner()
	ctx := context.Background()
	session := Session{Token: "user-token", UserID: "user-1", ServerID: "srv-1"}

	if err := p.AddLibrary(ctx, session, "Movies", "/media/movies", OnlyIfUnconfigured); !errors.Is(err, ErrAlreadyConfigured) {
		t.Errorf("AddLibrary without consent: err = %v, want ErrAlreadyConfigured", err)
	}
	if _, err := p.MintKey(ctx, session, KeyAppName, OnlyIfUnconfigured); !errors.Is(err, ErrAlreadyConfigured) {
		t.Errorf("MintKey without consent: err = %v, want ErrAlreadyConfigured", err)
	}
	for _, r := range fake.requests() {
		if r.route() == "POST "+pathVirtualFolders || r.route() == "POST "+pathAuthKeys {
			t.Errorf("wrote to a configured server without consent: %s", r.route())
		}
	}

	// With the opt-in, both work — that is the whole adopt branch, and it is
	// additive: nothing under /Startup/ runs either way.
	fake.forget()
	if err := p.AddLibrary(ctx, session, "Movies", "/media/movies", AdoptConfigured); err != nil {
		t.Errorf("AddLibrary with consent: %v", err)
	}
	key, err := p.MintKey(ctx, session, KeyAppName, AdoptConfigured)
	if err != nil {
		t.Errorf("MintKey with consent: %v", err)
	}
	if key == "" {
		t.Error("MintKey with consent returned an empty key")
	}
	for _, r := range fake.requests() {
		if strings.HasPrefix(r.Path, startupPrefix) {
			t.Errorf("the adopt branch touched %s, which it may never do", r.route())
		}
	}
}

// --- the library ------------------------------------------------------------

// PathInfos and nothing else. An unspecified boolean in LibraryOptions
// deserialises to false, and sending a fuller object is how ProviderIds.Tmdb
// gets switched off by accident — which breaks Open in Jellyfin for every film,
// silently, with the link still rendering (D32).
func TestAddLibrarySendsPathInfosAndNothingElse(t *testing.T) {
	fake := newFakeJellyfin(t)
	p := fake.provisioner()
	session := Session{Token: "user-token"}

	if err := p.AddLibrary(context.Background(), session, "Movies", "/media/movies", OnlyIfUnconfigured); err != nil {
		t.Fatalf("AddLibrary: %v", err)
	}

	var post recorded
	for _, r := range fake.requests() {
		if r.route() == "POST "+pathVirtualFolders {
			post = r
		}
	}
	if post.Body == nil {
		t.Fatal("no POST to /Library/VirtualFolders was recorded")
	}

	for key, want := range map[string]string{
		"name":           "Movies",
		"collectionType": "movies",
		// Measured: with refreshLibrary=false the folder came back with no
		// ItemId and no LibraryOptions until a refresh materialised it.
		"refreshLibrary": "true",
	} {
		if got := post.Query.Get(key); got != want {
			t.Errorf("query %s = %q, want %q", key, got, want)
		}
	}
	if post.Header.Get(tokenHeader) != "user-token" {
		t.Errorf("%s = %q, want the session token", tokenHeader, post.Header.Get(tokenHeader))
	}

	var body map[string]any
	if err := json.Unmarshal(post.Body, &body); err != nil {
		t.Fatalf("decoding the sent body: %v", err)
	}
	options, ok := body["LibraryOptions"].(map[string]any)
	if !ok {
		t.Fatalf("body = %s, want a LibraryOptions object", post.Body)
	}
	if len(options) != 1 {
		t.Errorf("LibraryOptions = %v, want PathInfos and no other key", options)
	}
	infos, ok := options["PathInfos"].([]any)
	if !ok || len(infos) != 1 {
		t.Fatalf("PathInfos = %v, want one entry", options["PathInfos"])
	}
	info, _ := infos[0].(map[string]any)
	if info["Path"] != "/media/movies" {
		t.Errorf("PathInfos[0] = %v, want the path curator writes to", info)
	}
	if _, present := options["EnableInternetProviders"]; present {
		t.Error("sent EnableInternetProviders — it reads false in the listing and is a red herring; " +
			"options.xml omits it, defaults apply, and metadata fetching runs anyway")
	}
}

// A 204 is not proof the library exists: a folder created with
// refreshLibrary=false came back in the listing with no ItemId and no
// LibraryOptions key at all until a refresh materialised it.
func TestAddLibraryFailsWhenTheListingDoesNotContainThePath(t *testing.T) {
	fake := newFakeJellyfin(t)
	fake.answer("POST "+pathVirtualFolders, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	fake.answer("GET "+pathVirtualFolders, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, []map[string]any{{"Name": "Movies", "Locations": []string{"/somewhere/else"}}})
	})

	err := fake.provisioner().AddLibrary(context.Background(), Session{Token: "t"}, "Movies", "/media/movies", OnlyIfUnconfigured)
	if err == nil {
		t.Fatal("a 204 with no library behind it reported success")
	}
	if !errors.Is(err, ErrUnexpectedResponse) {
		t.Errorf("err = %v, want ErrUnexpectedResponse so the screen can degrade to instructions", err)
	}
	if !strings.Contains(err.Error(), "/media/movies") {
		t.Errorf("err = %q, want it to name the path that is missing", err)
	}
}

// Measured on a real 10.10.7: POST /Library/VirtualFolders answered 404 and had
// created the library anyway. Jellyfin builds the folder and then refreshes it,
// and an exception in the refresh becomes the response status while the folder
// stays — so a status is a hint and the listing is the fact, in both directions.
//
// Reporting the failure without looking is worse than a wrong error message
// here: the obvious response to "that did not work" is to press the button
// again, and that is how a server ends up holding Movies, Movies2 and Movies3
// all pointing at one directory. Seen, on this laptop, three deep.
func TestAddLibraryBelievesTheListingRatherThanAFailedStatus(t *testing.T) {
	fake := newFakeJellyfin(t)
	fake.answer("POST "+pathVirtualFolders, func(w http.ResponseWriter, r *http.Request) {
		// Created, and then answered as though it were not.
		fake.mu.Lock()
		fake.folders = append(fake.folders, map[string]any{
			"Name": r.URL.Query().Get("name"), "Locations": []any{"/media/movies"},
		})
		fake.mu.Unlock()
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Error processing request."))
	})

	if err := fake.provisioner().AddLibrary(
		context.Background(), Session{Token: "t"}, "Movies", "/media/movies", OnlyIfUnconfigured,
	); err != nil {
		t.Errorf("AddLibrary: %v — the listing says the library is there", err)
	}
}

// And the other half: a status that failed and a listing that agrees it failed
// is still a failure, so the check above cannot be a way of never reporting one.
func TestAddLibraryStillFailsWhenTheLibraryReallyIsNotThere(t *testing.T) {
	fake := newFakeJellyfin(t)
	fake.answer("POST "+pathVirtualFolders, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("Error processing request."))
	})

	err := fake.provisioner().AddLibrary(
		context.Background(), Session{Token: "t"}, "Movies", "/media/movies", OnlyIfUnconfigured)
	if !errors.Is(err, ErrUnexpectedResponse) {
		t.Errorf("err = %v, want ErrUnexpectedResponse so the screen degrades to instructions", err)
	}
}

// A trailing slash on one side of the comparison is not a missing library.
func TestAddLibraryAcceptsATrailingSlashInTheListing(t *testing.T) {
	fake := newFakeJellyfin(t)
	fake.answer("GET "+pathVirtualFolders, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, []map[string]any{{"Name": "Movies", "Locations": []string{"/media/movies/"}}})
	})

	if err := fake.provisioner().AddLibrary(context.Background(), Session{Token: "t"}, "Movies", "/media/movies", OnlyIfUnconfigured); err != nil {
		t.Errorf("AddLibrary: %v", err)
	}
}

// --- what the adopt branch reads (T66) --------------------------------------

// A real server has Movies, Shows, Music and a folder somebody made in 2019.
// Libraries returns all of them: a caller that assumed one would describe the
// wrong server on every install this branch exists for.
func TestLibrariesReturnsEveryLibraryAndAssumesNothingAboutOne(t *testing.T) {
	fake := newFakeJellyfin(t)
	fake.complete(true)
	fake.answer("GET "+pathVirtualFolders, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, []map[string]any{
			{"Name": "Movies", "Locations": []string{"/movies"}},
			{"Name": "Shows", "Locations": []string{"/tv"}},
			{"Name": "Music", "Locations": []string{"/music", "/music-2"}},
			{"Name": "old stuff", "Locations": []string{}},
		})
	})

	got, err := fake.provisioner().Libraries(context.Background(), Session{Token: "t"})
	if err != nil {
		t.Fatalf("Libraries: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("got %d libraries, want 4 — every one of them", len(got))
	}
	if got[2].Name != "Music" || len(got[2].Locations) != 2 {
		t.Errorf("a library with two locations came back as %+v", got[2])
	}
	// A read, against a configured server, with no consent argument anywhere:
	// looking changes nothing and must not need permission.
	for _, r := range fake.requests() {
		if r.Method != http.MethodGet {
			t.Errorf("Libraries sent a %s", r.route())
		}
	}
}

// Covers is the hint, and its edges are where a wrong answer would be
// confidently wrong.
func TestCoversTakesAParentDirectoryAndNotAPrefix(t *testing.T) {
	for _, tc := range []struct {
		name     string
		location string
		path     string
		want     bool
	}{
		{"the same path", "/media/movies", "/media/movies", true},
		{"a trailing slash on the server's side", "/media/movies/", "/media/movies", true},
		{"a trailing slash on curator's side", "/media/movies", "/media/movies/", true},
		{"a parent, because jellyfin scans recursively", "/media", "/media/movies", true},
		{"the root", "/", "/media/movies", true},
		// The one a plain HasPrefix gets wrong, and it is not hypothetical:
		// /media/mov and /media/movies are the kind of pair a NAS has.
		{"a prefix that is not a parent", "/media/mov", "/media/movies", false},
		{"a child is not a cover", "/media/movies/kids", "/media/movies", false},
		{"a different mount", "/movies", "/media/storage/media/movies", false},
		{"nothing at all", "", "/media/movies", false},
		{"no path to look for", "/media/movies", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			library := Library{Name: "Movies", Locations: []string{tc.location}}
			if got := library.Covers(tc.path); got != tc.want {
				t.Errorf("Library{%q}.Covers(%q) = %v, want %v", tc.location, tc.path, got, tc.want)
			}
		})
	}
}

// PathExists separates "no library covers it, offer to add one" from "that
// server cannot see this path at all", which is the difference between a button
// and a sentence.
func TestPathExistsAnswersFromTheServersOwnFilesystem(t *testing.T) {
	fake := newFakeJellyfin(t)
	fake.complete(true)
	fake.paths["/media/movies"] = true
	p := fake.provisioner()
	ctx := context.Background()

	if got, err := p.PathExists(ctx, Session{Token: "t"}, "/media/movies"); err != nil || got != PathPresent {
		t.Errorf("PathExists(existing) = %v, %v; want present", got, err)
	}
	if got, err := p.PathExists(ctx, Session{Token: "t"}, "/media/storage/media/movies"); err != nil || got != PathAbsent {
		t.Errorf("PathExists(missing) = %v, %v; want absent", got, err)
	}

	// ValidateWritable is never sent true — the true branch writes a probe file
	// into somebody else's library, and the fake answers 403 to one so that a
	// change which started sending it fails here rather than on a NAS.
	for _, r := range fake.requests() {
		if r.Path != pathValidatePath {
			continue
		}
		if strings.Contains(string(r.Body), `"ValidateWritable":true`) {
			t.Errorf("asked jellyfin to prove a path is writable: %s", r.Body)
		}
	}
}

// The measurement this method is built around: Jellyfin's "that path is not
// there" 404 is byte-for-byte ASP.NET's "that route is not there" 404, so a
// version without the endpoint would otherwise read as a server that cannot see
// the path — a confident wrong sentence in the one branch that must not produce
// one.
func TestPathExistsWillNotCallAMissingEndpointAMissingPath(t *testing.T) {
	fake := newFakeJellyfin(t)
	fake.complete(true)
	fake.answer("POST "+pathValidatePath, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	got, err := fake.provisioner().PathExists(context.Background(), Session{Token: "t"}, "/media/movies")
	if err != nil {
		t.Fatalf("PathExists: %v — a server that cannot answer is not a failed adoption", err)
	}
	if got != PathUnknown {
		t.Errorf("PathExists = %v, want unknown: this server 404s a path that cannot be missing", got)
	}
	// Asked about "/" as well, which is the whole mechanism.
	var asked []string
	for _, r := range fake.requests() {
		if r.Path == pathValidatePath {
			asked = append(asked, string(r.Body))
		}
	}
	if len(asked) != 2 {
		t.Errorf("sent %d path checks (%v), want the path and then the root", len(asked), asked)
	}
}

// A rejected token is the caller's to report, not this method's to swallow.
func TestPathExistsReportsARejectedToken(t *testing.T) {
	fake := newFakeJellyfin(t)
	fake.complete(true)

	got, err := fake.provisioner().PathExists(context.Background(), Session{}, "/media/movies")
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
	if got != PathUnknown {
		t.Errorf("PathExists = %v, want unknown alongside the error", got)
	}
}

// --- the key ----------------------------------------------------------------

// POST /Auth/Keys is not idempotent — two posts made two keys both named
// curator, both with Id 0 — so the listing is read first and an existing key is
// reused rather than a second one accumulated on the user's server.
func TestMintKeyReusesAnExistingKeyRatherThanMintingASecond(t *testing.T) {
	fake := newFakeJellyfin(t)
	fake.seedKeys(map[string]any{
		"AccessToken": "already-there", "AppName": KeyAppName,
		"DateCreated": "2026-08-15T09:00:00.0000000Z", "Id": 0,
	})

	key, err := fake.provisioner().MintKey(context.Background(), Session{Token: "t"}, KeyAppName, OnlyIfUnconfigured)
	if err != nil {
		t.Fatalf("MintKey: %v", err)
	}
	if key != "already-there" {
		t.Errorf("key = %q, want the one already on the server", key)
	}
	if fake.mintCount() != 0 {
		t.Errorf("minted %d keys; a retried provision must not litter the server", fake.mintCount())
	}
}

// More than one is a real state rather than a defensive branch, so it has a
// defined answer and it is said out loud.
func TestMintKeyPicksTheNewestOfSeveralAndLogsThatItDid(t *testing.T) {
	fake := newFakeJellyfin(t)
	// The tokens are named so that none of them is a word the log message uses:
	// the assertion below is that no KEY reaches the log, and it would pass by
	// accident against a fixture called "newest".
	fake.seedKeys(
		map[string]any{"AccessToken": "token-older", "AppName": KeyAppName, "DateCreated": "2026-08-15T09:00:00.0000000Z"},
		map[string]any{"AccessToken": "token-newest", "AppName": KeyAppName, "DateCreated": "2026-08-15T11:30:00.0000000Z"},
		map[string]any{"AccessToken": "token-middle", "AppName": KeyAppName, "DateCreated": "2026-08-15T10:00:00.0000000Z"},
		map[string]any{"AccessToken": "token-somebody-elses", "AppName": "jellyseerr", "DateCreated": "2026-08-15T12:00:00.0000000Z"},
	)

	logged := &bytes.Buffer{}
	p := NewProvisioner(fake.http.URL, "0.1.0", fake.http.Client(),
		slog.New(slog.NewTextHandler(logged, &slog.HandlerOptions{Level: slog.LevelDebug})))

	key, err := p.MintKey(context.Background(), Session{Token: "t"}, KeyAppName, OnlyIfUnconfigured)
	if err != nil {
		t.Fatalf("MintKey: %v", err)
	}
	if key != "token-newest" {
		t.Errorf("key = %q, want the newest by DateCreated", key)
	}
	if !strings.Contains(logged.String(), "more than one") {
		t.Errorf("nothing was logged about the duplicates: %q", logged.String())
	}
	// The key itself is never logged, at any level: it goes through phase 7's
	// secret machinery and is encrypted at rest (D28).
	for _, secret := range []string{"token-newest", "token-older", "token-middle"} {
		if strings.Contains(logged.String(), secret) {
			t.Errorf("the log contains an API key: %q", logged.String())
		}
	}
}

// A key that was posted and then does not appear in the listing is an error and
// never an empty string, which would be stored as a configured-but-broken
// integration.
func TestMintKeyErrorsWhenTheKeyDoesNotAppear(t *testing.T) {
	fake := newFakeJellyfin(t)
	fake.answer("POST "+pathAuthKeys, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	fake.answer("GET "+pathAuthKeys, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"Items": []any{}})
	})

	key, err := fake.provisioner().MintKey(context.Background(), Session{Token: "t"}, KeyAppName, OnlyIfUnconfigured)
	if err == nil {
		t.Fatal("a key that was never listed reported success")
	}
	if key != "" {
		t.Errorf("key = %q, want empty alongside the error", key)
	}
	if !errors.Is(err, ErrUnexpectedResponse) {
		t.Errorf("err = %v, want ErrUnexpectedResponse", err)
	}
}

// /Auth/Keys needs a credential even while the wizard is open — unauthenticated
// it is 401, measured — so it is the one step that can fail on the token rather
// than on the server, and it says so with the sentinel this package already has.
func TestMintKeyReportsARejectedTokenAsErrUnauthorized(t *testing.T) {
	fake := newFakeJellyfin(t)
	fake.answer("GET "+pathAuthKeys, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})

	_, err := fake.provisioner().MintKey(context.Background(), Session{Token: "stale"}, KeyAppName, OnlyIfUnconfigured)
	if !errors.Is(err, ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}

// --- authenticating ---------------------------------------------------------

// 401 and 400 mean opposite things and the difference reaches the user: telling
// someone their password is wrong when curator sent a malformed header is the
// worst available message, because the password is the one thing they will
// retype for ever.
func TestAuthenticateSeparatesAWrongPasswordFromARejectedRequest(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   error
		other  error
	}{
		{http.StatusUnauthorized, ErrBadCredentials, ErrAuthRequestRejected},
		{http.StatusBadRequest, ErrAuthRequestRejected, ErrBadCredentials},
	} {
		fake := newFakeJellyfin(t)
		fake.answer("POST "+pathAuthenticateByName, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
		})

		_, err := fake.provisioner().Authenticate(context.Background(), "nethmin", "hunter2")
		if !errors.Is(err, tc.want) {
			t.Errorf("status %d: err = %v, want %v", tc.status, err, tc.want)
		}
		if errors.Is(err, tc.other) {
			t.Errorf("status %d: err also matched %v — the two are indistinguishable", tc.status, tc.other)
		}
	}
}

// The header whose absence produces that 400 in the first place.
func TestAuthenticateSendsAWellFormedMediaBrowserHeader(t *testing.T) {
	fake := newFakeJellyfin(t)
	p := fake.provisioner()

	if _, err := p.Authenticate(context.Background(), "nethmin", "hunter2"); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}

	var got recorded
	for _, r := range fake.requests() {
		if r.route() == "POST "+pathAuthenticateByName {
			got = r
		}
	}
	header := got.Header.Get("Authorization")
	if !strings.HasPrefix(header, "MediaBrowser ") {
		t.Fatalf("Authorization = %q, want the MediaBrowser scheme", header)
	}
	for _, field := range []string{"Client", "Device", "DeviceId", "Version"} {
		// Quoted, and non-empty: Jellyfin answers 400 to the header rather than
		// ignoring a field it cannot parse.
		if !strings.Contains(header, field+`="`) || strings.Contains(header, field+`=""`) {
			t.Errorf("Authorization = %q, want a non-empty quoted %s", header, field)
		}
	}
	if !strings.Contains(header, `Version="0.1.0"`) {
		t.Errorf("Authorization = %q, want curator's version — the one cmd/curator declares", header)
	}

	// The password is in the body as Pw, and never in the URL or a header.
	if strings.Contains(got.Query.Encode(), "hunter2") || strings.Contains(header, "hunter2") {
		t.Error("the password leaked out of the body")
	}
	var body map[string]string
	if err := json.Unmarshal(got.Body, &body); err != nil {
		t.Fatalf("decoding the sent body: %v", err)
	}
	if body["Username"] != "nethmin" || body["Pw"] != "hunter2" {
		t.Errorf("body = %v, want Username and Pw — 10.10.7 takes Pw", body)
	}
}

// A 200 with no AccessToken is not a session, and returning one with an empty
// token would fail three calls later somewhere unrelated.
func TestAuthenticateRejectsA200WithNoToken(t *testing.T) {
	fake := newFakeJellyfin(t)
	fake.answer("POST "+pathAuthenticateByName, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"ServerId": "srv-1"})
	})

	_, err := fake.provisioner().Authenticate(context.Background(), "nethmin", "hunter2")
	if !errors.Is(err, ErrUnexpectedResponse) {
		t.Errorf("err = %v, want ErrUnexpectedResponse", err)
	}
}

// --- degrading to instructions ----------------------------------------------

// The startup endpoints are what the wizard happens to call in the version we
// pin, not a contract. Every step has to survive a later Jellyfin answering
// something else — with a typed error the screen turns into manual steps, not a
// panic and not a generic failure.
func TestAnUnexpectedShapeAtAnyStepIsTheInstructionsError(t *testing.T) {
	garbage := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `<html>this is not the api you are looking for</html>`)
	}

	for name, tc := range map[string]struct {
		route string
		call  func(*Provisioner) error
	}{
		"System/Info/Public": {"GET " + pathSystemInfoPublic, func(p *Provisioner) error {
			_, err := p.Status(context.Background())
			return err
		}},
		"Startup/Configuration": {"GET " + pathStartupConfiguration, func(p *Provisioner) error {
			return p.Configure(context.Background(), StartupConfiguration{})
		}},
		"Startup/User": {"GET " + pathStartupUser, func(p *Provisioner) error {
			return p.CreateAdmin(context.Background(), "nethmin", "hunter2")
		}},
		"AuthenticateByName": {"POST " + pathAuthenticateByName, func(p *Provisioner) error {
			_, err := p.Authenticate(context.Background(), "nethmin", "hunter2")
			return err
		}},
		"Library/VirtualFolders": {"GET " + pathVirtualFolders, func(p *Provisioner) error {
			return p.AddLibrary(context.Background(), Session{Token: "t"}, "Movies", "/media/movies", OnlyIfUnconfigured)
		}},
		"Auth/Keys": {"GET " + pathAuthKeys, func(p *Provisioner) error {
			_, err := p.MintKey(context.Background(), Session{Token: "t"}, KeyAppName, OnlyIfUnconfigured)
			return err
		}},
	} {
		fake := newFakeJellyfin(t)
		fake.answer(tc.route, garbage)

		err := tc.call(fake.provisioner())
		if !errors.Is(err, ErrUnexpectedResponse) {
			t.Errorf("%s: err = %v, want ErrUnexpectedResponse", name, err)
		}
	}
}

// A status outside the measured table is the same degrade path, and the error
// says which endpoint and which status so the manual steps can name them.
func TestAnUnexpectedStatusIsTheInstructionsError(t *testing.T) {
	fake := newFakeJellyfin(t)
	fake.answer("POST "+pathStartupUser, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not implemented", http.StatusNotImplemented)
	})

	err := fake.provisioner().CreateAdmin(context.Background(), "nethmin", "hunter2")
	if !errors.Is(err, ErrUnexpectedResponse) {
		t.Fatalf("err = %v, want ErrUnexpectedResponse", err)
	}
	if !strings.Contains(err.Error(), "501") || !strings.Contains(err.Error(), pathStartupUser) {
		t.Errorf("err = %q, want it to name the status and the endpoint", err)
	}
}

// Something that answers 200 with JSON and is not a Jellyfin — a proxy, a
// router's admin page, curator itself — must be caught by the guard's own read
// rather than by the first write.
func TestStatusRefusesSomethingThatIsNotAJellyfin(t *testing.T) {
	fake := newFakeJellyfin(t)
	fake.answer("GET "+pathSystemInfoPublic, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"ok": true})
	})

	_, err := fake.provisioner().Status(context.Background())
	if !errors.Is(err, ErrUnexpectedResponse) {
		t.Errorf("err = %v, want ErrUnexpectedResponse", err)
	}
}

// A whole HTML error page from an unwell proxy does not belong in an error
// string, exactly as for the client's own calls.
func TestAnErrorPageIsCappedAndFlattened(t *testing.T) {
	fake := newFakeJellyfin(t)
	fake.answer("GET "+pathSystemInfoPublic, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "<html>\n<head><title>502</title></head>\n<body>"+
			strings.Repeat("x", 8000)+"</body>\n</html>")
	})

	_, err := fake.provisioner().Status(context.Background())
	if err == nil {
		t.Fatal("a 502 reported success")
	}
	if len(err.Error()) > 512 {
		t.Errorf("error is %d bytes; the page was pasted in whole", len(err.Error()))
	}
	if strings.Contains(err.Error(), "\n") {
		t.Error("the error spans multiple lines")
	}
}

// --- reachability and construction ------------------------------------------

// The container takes a measured 17.6 s to answer its first request, so "not
// there yet" is a state the screen waits in rather than a failure it reports.
func TestAnUnreachableJellyfinIsItsOwnError(t *testing.T) {
	// Port 9 is discard; nothing listens.
	const dead = "http://127.0.0.1:9"
	p := NewProvisioner(dead, "0.1.0", &http.Client{Timeout: 2 * time.Second}, discardLogger())

	_, err := p.Status(context.Background())
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v, want ErrUnreachable", err)
	}
	if errors.Is(err, ErrUnexpectedResponse) || errors.Is(err, ErrAlreadyConfigured) {
		t.Errorf("err = %v, want only ErrUnreachable — a container that is still starting is not a broken API", err)
	}
	if !strings.Contains(err.Error(), dead) {
		t.Errorf("err = %q, want it to name the URL that could not be reached", err)
	}
}

// A Jellyfin that is loading answers 503 rather than refusing the connection,
// and that is a state to wait in and not a server to give up on.
//
// Measured while provisioning a throwaway container: for most of the 17.6 s
// cold start, /System/Info/Public answers 503 with this exact body. Mapping it
// to the degrade path would put "curator does not understand this server, here
// are the manual steps" on the screen for the whole time the container the user
// was just told to start is starting.
func TestAStartingJellyfinIsNotABrokenOne(t *testing.T) {
	fake := newFakeJellyfin(t)
	fake.answer("GET "+pathSystemInfoPublic, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "Jellyfin Server is loading. Please try again shortly.")
	})

	_, err := fake.provisioner().Status(context.Background())
	if !errors.Is(err, ErrNotReady) {
		t.Fatalf("err = %v, want ErrNotReady", err)
	}
	if errors.Is(err, ErrUnexpectedResponse) {
		t.Error("a starting Jellyfin was reported as one curator does not understand")
	}
	// Distinct from ErrUnreachable on purpose: when the wait finally times out,
	// "nothing is listening" and "still loading" are different sentences.
	if errors.Is(err, ErrUnreachable) {
		t.Error("a 503 was reported as an unreachable host; something did answer")
	}
}

// The transport error survives alongside the sentinel, so a caller can still
// tell a refused connection from a deadline.
func TestUnreachableKeepsTheUnderlyingError(t *testing.T) {
	fake := newFakeJellyfin(t)
	p := fake.provisioner()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := p.Status(ctx)
	if !errors.Is(err, ErrUnreachable) {
		t.Fatalf("err = %v, want ErrUnreachable", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled to survive the wrapping", err)
	}
}

func TestNewProvisionerDefaults(t *testing.T) {
	p := NewProvisioner("", "0.1.0", nil, nil)
	if p.baseURL != DefaultBaseURL {
		t.Errorf("baseURL = %q, want %q", p.baseURL, DefaultBaseURL)
	}
	if p.http == http.DefaultClient {
		t.Fatal("a nil http.Client became http.DefaultClient, which has no timeout")
	}
	if p.http.Timeout <= 0 {
		t.Errorf("timeout = %v, want a positive one", p.http.Timeout)
	}
	if p.log == nil {
		t.Error("log is nil; MintKey logs and would panic")
	}

	// A trailing slash is easy to type into a settings field, and a doubled
	// slash is a 404 from a server that would otherwise have worked.
	if got := NewProvisioner("http://jellyfin:8096/", "0.1.0", nil, nil).baseURL; got != "http://jellyfin:8096" {
		t.Errorf("baseURL = %q, want the slash trimmed", got)
	}
}

// An empty version must still produce a parseable header rather than
// Version="", which is the shape that earns the 400 this all exists to avoid.
func TestTheAuthorizationHeaderSurvivesAnEmptyVersion(t *testing.T) {
	header := NewProvisioner("http://x:8096", "", nil, nil).authorization()
	if strings.Contains(header, `Version=""`) {
		t.Errorf("Authorization = %q, want a non-empty version", header)
	}
}
