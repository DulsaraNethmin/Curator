package api

// The Playback screen's two endpoints, against a fake Provisioner.
//
// Hermetic on purpose: internal/jellyfin already proves the request sequence
// against a fake Jellyfin, and proving it twice here would test that package's
// wire format from a place that cannot see it. What is asserted here is what
// this package owns — which of the four worlds the probe reports, that the
// sequence stops at the first failure, that every failure ends at instructions
// carrying the real library path, and that a success writes exactly the four
// settings and never sends the key back.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/DulsaraNethmin/curator/internal/jellyfin"
	"github.com/DulsaraNethmin/curator/internal/settings"
)

const (
	testJellyfinURL = "http://jellyfin:8096"
	testLibraryPath = "/media/movies"
)

// fakeProvisioner records the sequence and fails wherever it is told to.
//
// calls is the order the steps actually ran in, which is the only way to assert
// the property T64's ordering exists for: CompleteSetup is last, so a failure
// above it leaves the server visibly unfinished rather than finished and wrong.
type fakeProvisioner struct {
	status    jellyfin.ServerStatus
	statusErr error

	// failAt is the method name that returns failWith; everything before it
	// succeeds.
	failAt   string
	failWith error

	key   string
	calls []string

	// The adopt branch's two reads, and what the fake server holds.
	libraries  []jellyfin.Library
	pathAnswer jellyfin.PathAnswer
	pathErr    error

	// consents records the Consent every additive call was made with, so a
	// change that started adopting from the setup flow — or setting up from the
	// adopt flow — fails here.
	consents []jellyfin.Consent
}

func (f *fakeProvisioner) record(name string) error {
	f.calls = append(f.calls, name)
	if f.failAt == name {
		return f.failWith
	}
	return nil
}

func (f *fakeProvisioner) Status(context.Context) (jellyfin.ServerStatus, error) {
	return f.status, f.statusErr
}

func (f *fakeProvisioner) Configure(context.Context, jellyfin.StartupConfiguration) error {
	return f.record("Configure")
}

func (f *fakeProvisioner) CreateAdmin(_ context.Context, name, password string) error {
	if name == "" || password == "" {
		return fmt.Errorf("CreateAdmin got an empty credential: %q/%q", name, password)
	}
	return f.record("CreateAdmin")
}

func (f *fakeProvisioner) EnableRemoteAccess(context.Context) error {
	return f.record("EnableRemoteAccess")
}

func (f *fakeProvisioner) Authenticate(context.Context, string, string) (jellyfin.Session, error) {
	if err := f.record("Authenticate"); err != nil {
		return jellyfin.Session{}, err
	}
	return jellyfin.Session{Token: "session-token", UserID: "user-id"}, nil
}

func (f *fakeProvisioner) AddLibrary(
	_ context.Context, session jellyfin.Session, name, libraryPath string, consent jellyfin.Consent,
) error {
	if session.Token == "" {
		return errors.New("AddLibrary ran without a session")
	}
	if libraryPath != testLibraryPath {
		return fmt.Errorf("AddLibrary got path %q, want %q", libraryPath, testLibraryPath)
	}
	f.consents = append(f.consents, consent)
	if err := f.record("AddLibrary"); err != nil {
		return err
	}
	f.libraries = append(f.libraries, jellyfin.Library{Name: name, Locations: []string{libraryPath}})
	return nil
}

func (f *fakeProvisioner) MintKey(
	_ context.Context, session jellyfin.Session, app string, consent jellyfin.Consent,
) (string, error) {
	if session.Token == "" {
		return "", errors.New("MintKey ran without a session")
	}
	f.consents = append(f.consents, consent)
	_ = app
	if err := f.record("MintKey"); err != nil {
		return "", err
	}
	return f.key, nil
}

func (f *fakeProvisioner) CompleteSetup(context.Context) error {
	return f.record("CompleteSetup")
}

func (f *fakeProvisioner) Libraries(context.Context, jellyfin.Session) ([]jellyfin.Library, error) {
	if err := f.record("Libraries"); err != nil {
		return nil, err
	}
	return f.libraries, nil
}

func (f *fakeProvisioner) PathExists(_ context.Context, _ jellyfin.Session, path string) (jellyfin.PathAnswer, error) {
	if path != testLibraryPath {
		return jellyfin.PathUnknown, fmt.Errorf("PathExists got %q, want %q", path, testLibraryPath)
	}
	if err := f.record("PathExists"); err != nil {
		return jellyfin.PathUnknown, err
	}
	return f.pathAnswer, f.pathErr
}

// only reports whether every additive call was made with one consent, which is
// how the two flows are held apart: the setup flow may never adopt, and the
// adopt flow may never run as though the server were curator's.
func (f *fakeProvisioner) only(want jellyfin.Consent) bool {
	for _, got := range f.consents {
		if got != want {
			return false
		}
	}
	return len(f.consents) > 0
}

// fakeReader is curator's own read-only client, as the adopt branch's final
// check uses it. It remembers the base URL and the key it was built with,
// because "the key it just minted, against the server it was just told about"
// is the property that check exists to have.
type fakeReader struct {
	baseURL string
	key     string
	item    jellyfin.Item
	err     error
	calls   int
}

func (r *fakeReader) FindMovie(context.Context, int, int) (jellyfin.Item, error) {
	r.calls++
	return r.item, r.err
}

// setupServer wires the Playback screen over a fake Provisioner and a fake
// settings writer, exactly as cmd/curator wires the real ones.
func setupServer(t *testing.T, p *fakeProvisioner, states map[string]SettingState) (http.Handler, *fakeWriter) {
	t.Helper()
	h, writer, _ := setupServerWithReader(t, p, states, &fakeReader{err: jellyfin.ErrNotFound})
	return h, writer
}

func setupServerWithReader(
	t *testing.T, p *fakeProvisioner, states map[string]SettingState, reader *fakeReader,
) (http.Handler, *fakeWriter, *fakeReader) {
	t.Helper()
	writer := &fakeWriter{}
	set := fullSettings()
	set.States = func(context.Context) (map[string]SettingState, error) { return states, nil }
	set.Writer = writer

	mux := http.NewServeMux()
	New(newFakeStore(), ScannerFunc(nil), nil, fixtureRoot, quiet()).
		WithSettings(set).
		WithJellyfinSetup(JellyfinSetup{
			URL:         testJellyfinURL,
			LibraryPath: testLibraryPath,
			New:         func(string) Provisioner { return p },
			Reader: func(baseURL, apiKey string) MediaServer {
				reader.baseURL, reader.key = baseURL, apiKey
				return reader
			},
		}).
		RegisterJellyfin(mux)
	return mux, writer, reader
}

func probe(t *testing.T, h http.Handler) (*httptest.ResponseRecorder, jellyfinProbeBody) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/jellyfin/probe", nil))
	var body jellyfinProbeBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("probe body %q: %v", rec.Body.String(), err)
	}
	return rec, body
}

func postProvision(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/jellyfin/provision", strings.NewReader(body)))
	return rec
}

func failureBody(t *testing.T, rec *httptest.ResponseRecorder) jellyfinFailureBody {
	t.Helper()
	var body jellyfinFailureBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failure body %q: %v", rec.Body.String(), err)
	}
	return body
}

// The probe's four worlds. Three of them are branches on the screen and the
// fourth — starting — shares a branch with unreachable and a sentence with
// nothing: "the command did not run" and "it ran and is loading" are the two
// things somebody watching a spinner actually wants told apart.
func TestProbeReportsWhichWorldTheJellyfinIsIn(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    jellyfin.ServerStatus
		statusErr error
		want      string
	}{
		{"never set up", jellyfin.ServerStatus{Version: "10.10.7"}, nil, probeNeedsSetup},
		{"already set up",
			jellyfin.ServerStatus{Version: "10.10.7", WizardCompleted: true}, nil, probeConfigured},
		{"nothing listening", jellyfin.ServerStatus{}, jellyfin.ErrUnreachable, probeUnreachable},
		{"up and loading", jellyfin.ServerStatus{}, jellyfin.ErrNotReady, probeStarting},
		{"not a jellyfin", jellyfin.ServerStatus{}, jellyfin.ErrUnexpectedResponse, probeUnreachable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := setupServer(t, &fakeProvisioner{status: tc.status, statusErr: tc.statusErr}, nil)
			rec, body := probe(t, h)

			// Always 200: the state IS the answer. A probe that 503s while a
			// container boots makes the expected state look like a broken
			// curator in a browser's network tab.
			if rec.Code != http.StatusOK {
				t.Errorf("status = %d, want 200 — the state is the answer, not the failure", rec.Code)
			}
			if body.State != tc.want {
				t.Errorf("state = %q, want %q", body.State, tc.want)
			}
			if body.LibraryPath != testLibraryPath {
				t.Errorf("library_path = %q, want %q — the screen shows it read-only and must not have its own copy",
					body.LibraryPath, testLibraryPath)
			}
			if body.Command == "" || !strings.Contains(body.Command, "--profile jellyfin") {
				t.Errorf("command = %q, want the pasted line naming compose.yaml's profile", body.Command)
			}
			if tc.statusErr == nil && body.Version != "10.10.7" {
				t.Errorf("version = %q, want the version the server reported", body.Version)
			}
		})
	}
}

// The happy path: the measured sequence in the measured order, then exactly the
// settings phase 7 already has rows for.
func TestProvisionRunsTheSequenceAndRecordsTheResult(t *testing.T) {
	p := &fakeProvisioner{key: "minted-key-32-chars-long-xxxxxxx"}
	h, writer := setupServer(t, p, nil)

	rec := postProvision(t, h,
		`{"username":"nethmin","password":"hunter2","public_url":"http://192.168.1.80:8096"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	want := []string{
		"Configure", "CreateAdmin", "EnableRemoteAccess",
		"Authenticate", "AddLibrary", "MintKey", "CompleteSetup",
	}
	if strings.Join(p.calls, ",") != strings.Join(want, ",") {
		t.Errorf("sequence = %v, want %v", p.calls, want)
	}
	// The property the order exists for, asserted rather than assumed: the
	// library and the key are both in place before the wizard is closed, so a
	// failure at either leaves a server that is visibly unfinished.
	if p.calls[len(p.calls)-1] != "CompleteSetup" {
		t.Error("CompleteSetup must be last: a failure after it leaves a server finished and wrong")
	}

	if writer.calls != 1 {
		t.Fatalf("WriteSettings called %d times, want 1 — it is one transaction", writer.calls)
	}
	for key, want := range map[string]string{
		"jellyfin_url":        testJellyfinURL,
		"jellyfin_api_key":    "minted-key-32-chars-long-xxxxxxx",
		"jellyfin_public_url": "http://192.168.1.80:8096",
		"playback_target":     "jellyfin",
	} {
		if got := writer.set[key]; got != want {
			t.Errorf("wrote %s = %q, want %q", key, got, want)
		}
	}
	if len(writer.set) != 4 {
		t.Errorf("wrote %d settings (%v), want exactly the four this flow owns", len(writer.set), writer.set)
	}
	// The setup flow may never adopt. It runs against a server curator started,
	// and the zero Consent is what makes every additive call refuse one that has
	// already been set up.
	if !p.only(jellyfin.OnlyIfUnconfigured) {
		t.Errorf("the setup flow made an additive call with consent %v", p.consents)
	}
}

// The one thing this endpoint must never do.
func TestProvisionNeverSendsTheKeyBack(t *testing.T) {
	const key = "minted-key-32-chars-long-xxxxxxx"
	p := &fakeProvisioner{key: key}
	h, _ := setupServer(t, p, nil)

	rec := postProvision(t, h, `{"username":"nethmin","password":"hunter2"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	// The whole body, not the decoded struct: a field added later that happens
	// to carry the key would pass a struct assertion and fail this one.
	if strings.Contains(rec.Body.String(), key) {
		t.Errorf("the response carries the api key: %s", rec.Body.String())
	}
	// Not even a prefix. A masked secret still confirms an existence and a
	// length to anyone on the LAN (D17, D28).
	if strings.Contains(rec.Body.String(), key[:8]) {
		t.Errorf("the response carries a prefix of the api key: %s", rec.Body.String())
	}
}

// An empty public_url is a legitimate answer — "the browser reaches Jellyfin
// where curator does" — and must not be written as an empty row, because empty
// is what the setting already means.
func TestProvisionOmitsAnUnansweredPublicURL(t *testing.T) {
	h, writer := setupServer(t, &fakeProvisioner{key: "k"}, nil)

	if rec := postProvision(t, h, `{"username":"a","password":"b"}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if _, ok := writer.set["jellyfin_public_url"]; ok {
		t.Errorf("wrote jellyfin_public_url = %q when the browser sent none",
			writer.set["jellyfin_public_url"])
	}
}

// Every failure ends somewhere the user can still act, and every failure that
// leaves a reachable Jellyfin names the path — which is the step people get
// wrong when they finish by hand.
//
// The two transport failures deliberately do NOT name it. "Add a Movies library
// at /media/movies" is not a step anybody can take against a container that is
// not running, and printing it there would bury the one instruction that is
// actionable — run the command — under three that are not.
func TestEveryProvisionFailureDegradesToInstructions(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failAt   string
		err      error
		wantCode int
		wantStep string

		// wantPath is whether finishing by hand is possible from here, which is
		// exactly whether something answered.
		wantPath bool

		// wantSays is the one string the instructions must carry when the
		// manual route is not the answer. The two transport failures differ
		// here and that difference is the point: nothing listening means the
		// pasted command did not run, and still-loading means it did.
		wantSays string
	}{
		{"the locale call", "Configure", jellyfin.ErrUnexpectedResponse, http.StatusBadGateway, "locale", true, ""},
		{"creating the admin", "CreateAdmin", jellyfin.ErrUnexpectedResponse, http.StatusBadGateway, "the administrator", true, ""},
		{"remote access", "EnableRemoteAccess", jellyfin.ErrUnexpectedResponse, http.StatusBadGateway, "remote access", true, ""},
		{"signing in", "Authenticate", jellyfin.ErrBadCredentials, http.StatusUnprocessableEntity, "signing in", true, ""},
		{"the library", "AddLibrary", jellyfin.ErrUnexpectedResponse, http.StatusBadGateway, "the library", true, ""},
		{"minting the key", "MintKey", jellyfin.ErrUnauthorized, http.StatusBadGateway, "the API key", true, ""},
		{"closing the wizard", "CompleteSetup", jellyfin.ErrUnexpectedResponse, http.StatusBadGateway, "finishing the wizard", true, ""},
		{"the container went away", "Configure", jellyfin.ErrUnreachable, http.StatusServiceUnavailable, "locale", false, bundleCommand},
		{"it is still loading", "Configure", jellyfin.ErrNotReady, http.StatusServiceUnavailable, "locale", false, "still loading"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakeProvisioner{key: "k", failAt: tc.failAt, failWith: tc.err}
			h, writer := setupServer(t, p, nil)

			rec := postProvision(t, h, `{"username":"a","password":"b"}`)
			if rec.Code != tc.wantCode {
				t.Errorf("status = %d, want %d: %s", rec.Code, tc.wantCode, rec.Body.String())
			}
			body := failureBody(t, rec)
			if body.Step != tc.wantStep {
				t.Errorf("step = %q, want %q — the screen says how far curator got", body.Step, tc.wantStep)
			}
			if len(body.Instructions) == 0 {
				t.Fatal("no instructions: every failure has to end somewhere the user can still act")
			}
			joined := strings.Join(body.Instructions, "\n")
			switch {
			case tc.wantPath && !strings.Contains(joined, testLibraryPath):
				t.Errorf("instructions do not name %q, which is the step people get wrong: %v",
					testLibraryPath, body.Instructions)
			case !tc.wantPath && strings.Contains(joined, testLibraryPath):
				t.Errorf("instructions tell the user to add a library to a jellyfin that is not answering: %v",
					body.Instructions)
			case !tc.wantPath && !strings.Contains(joined, tc.wantSays):
				t.Errorf("instructions do not say %q: %v", tc.wantSays, body.Instructions)
			}

			// Nothing was recorded, because nothing was achieved. The one
			// exception is a write that fails, which has its own test.
			if writer.calls != 0 {
				t.Errorf("WriteSettings called %d times after a failed provision", writer.calls)
			}

			// The sequence stopped where it failed. A flow that carried on
			// would close the wizard over a server missing its library.
			if last := p.calls[len(p.calls)-1]; last != tc.failAt {
				t.Errorf("ran on to %q after %q failed", last, tc.failAt)
			}
		})
	}
}

// A server somebody is already watching is not a failure to retry — it is
// T66's branch, and the body says so with a flag rather than a message the
// screen would have to match on.
func TestProvisionRefusesAConfiguredServerAndOffersAdoption(t *testing.T) {
	p := &fakeProvisioner{
		key: "k", failAt: "Configure", failWith: jellyfin.ErrAlreadyConfigured,
	}
	h, writer := setupServer(t, p, nil)

	rec := postProvision(t, h, `{"username":"a","password":"b"}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
	body := failureBody(t, rec)
	if !body.Adopt {
		t.Error("adopt = false: the screen has to know this is T66's branch and not a retry")
	}
	if writer.calls != 0 {
		t.Errorf("WriteSettings called %d times against a server curator refused to touch", writer.calls)
	}
}

// D28's sharp edge, caught before Jellyfin is touched rather than after.
//
// A curator that set a server up and then found it could not record the key
// would leave a configured Jellyfin it cannot talk to, and no way back short of
// deleting a volume.
func TestProvisionRefusesWhenTheEnvironmentOwnsTheSettings(t *testing.T) {
	p := &fakeProvisioner{key: "k"}
	h, writer := setupServer(t, p, map[string]SettingState{
		"jellyfin_api_key": {Source: settings.SourceEnv, Configured: true},
	})

	rec := postProvision(t, h, `{"username":"a","password":"b"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if len(p.calls) != 0 {
		t.Errorf("touched jellyfin (%v) before finding out it could not record the result", p.calls)
	}
	if writer.calls != 0 {
		t.Errorf("WriteSettings called %d times", writer.calls)
	}
	if body := failureBody(t, rec); !strings.Contains(body.Error, "JELLYFIN_API_KEY") {
		t.Errorf("error does not name the variable that shadows it: %q", body.Error)
	}
}

// The write failing is the one case where Jellyfin IS configured and curator is
// not. It has to say exactly that, because the recovery is to paste the key by
// hand and nobody can guess it from "internal error".
func TestProvisionSaysSoWhenJellyfinIsUpAndTheWriteFailed(t *testing.T) {
	p := &fakeProvisioner{key: "k"}
	h, writer := setupServer(t, p, nil)
	writer.err = errors.New("database is locked")

	rec := postProvision(t, h, `{"username":"a","password":"b"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	body := failureBody(t, rec)
	if body.Step != "record" {
		t.Errorf("step = %q, want record", body.Step)
	}
	joined := strings.Join(body.Instructions, "\n")
	if !strings.Contains(joined, "API Keys") {
		t.Errorf("instructions do not say where to find the key by hand: %v", body.Instructions)
	}
	if !strings.Contains(joined, testJellyfinURL) {
		t.Errorf("instructions do not name the url to set: %v", body.Instructions)
	}
}

func TestProvisionRefusesAnIncompleteBody(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"no username", `{"password":"b"}`},
		{"no password", `{"username":"a"}`},
		{"blank username", `{"username":"   ","password":"b"}`},
		{"not an object", `"nope"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakeProvisioner{key: "k"}
			h, _ := setupServer(t, p, nil)
			if rec := postProvision(t, h, tc.body); rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			if len(p.calls) != 0 {
				t.Errorf("touched jellyfin with an incomplete body: %v", p.calls)
			}
		})
	}
}

// A process with no way to reach a Jellyfin says so rather than pretending.
func TestTheEndpointsRefuseWithoutASetup(t *testing.T) {
	mux := http.NewServeMux()
	New(newFakeStore(), ScannerFunc(nil), nil, fixtureRoot, quiet()).RegisterJellyfin(mux)

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/jellyfin/probe"},
		{http.MethodPost, "/api/jellyfin/provision"},
		{http.MethodPost, "/api/jellyfin/adopt"},
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}")))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d, want 503", tc.method, tc.path, rec.Code)
		}
	}
}

// --- T66: adopting a Jellyfin somebody already runs -------------------------

const testAdoptURL = "http://192.168.1.26:8096"

// adopted is a fake standing at the state this branch starts from: a server
// whose wizard somebody else finished.
func adopted(key string) *fakeProvisioner {
	return &fakeProvisioner{
		key:        key,
		status:     jellyfin.ServerStatus{Version: "10.10.7", WizardCompleted: true},
		pathAnswer: jellyfin.PathPresent,
	}
}

func postAdopt(t *testing.T, h http.Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/jellyfin/adopt", strings.NewReader(body)))
	return rec
}

func adoptedBody(t *testing.T, rec *httptest.ResponseRecorder) jellyfinAdoptedBody {
	t.Helper()
	var body jellyfinAdoptedBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("adopt body %q: %v", rec.Body.String(), err)
	}
	return body
}

// The task this whole branch exists for: connect, and change nothing.
func TestAdoptSignsInMintsAKeyAndTouchesNothingElse(t *testing.T) {
	p := adopted("adopted-key-32-chars-long-xxxxxx")
	p.libraries = []jellyfin.Library{
		{Name: "Movies", Locations: []string{"/movies"}},
		{Name: "Shows", Locations: []string{"/tv"}},
	}
	h, writer, reader := setupServerWithReader(t, p, nil, &fakeReader{err: jellyfin.ErrNotFound})

	rec := postAdopt(t, h, `{"url":"`+testAdoptURL+`","username":"nethmin","password":"hunter2",
		"public_url":"`+testAdoptURL+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	// Nothing from the wizard, in any order, ever. This is the branch where
	// being wrong renames somebody's admin account and changes its password.
	for _, call := range p.calls {
		switch call {
		case "Configure", "CreateAdmin", "EnableRemoteAccess", "CompleteSetup":
			t.Errorf("adoption called %s against a server somebody is watching", call)
		}
	}
	// And every additive call said so outright.
	if !p.only(jellyfin.AdoptConfigured) {
		t.Errorf("an additive call ran without the adopt opt-in: %v", p.consents)
	}

	body := adoptedBody(t, rec)
	if body.Library.State != libraryAddable {
		t.Errorf("library state = %q, want %q — no library covers curator's path and the server can see it",
			body.Library.State, libraryAddable)
	}
	if body.Library.Added {
		t.Error("added a library without being asked")
	}
	if strings.Join(body.Library.Names, ",") != "Movies,Shows" {
		t.Errorf("names = %v, want every library the server holds", body.Library.Names)
	}

	// The check ran with the key that was just minted, against the server that
	// was just typed — not with whatever this process started with.
	if reader.calls != 1 {
		t.Errorf("curator's own read ran %d times, want once before saying done", reader.calls)
	}
	if reader.key != "adopted-key-32-chars-long-xxxxxx" || reader.baseURL != testAdoptURL {
		t.Errorf("checked with key %q at %q, want the minted key at the typed url", reader.key, reader.baseURL)
	}

	for key, want := range map[string]string{
		"jellyfin_url":        testAdoptURL,
		"jellyfin_api_key":    "adopted-key-32-chars-long-xxxxxx",
		"jellyfin_public_url": testAdoptURL,
		"playback_target":     "jellyfin",
	} {
		if got := writer.set[key]; got != want {
			t.Errorf("wrote %s = %q, want %q", key, got, want)
		}
	}
	if strings.Contains(rec.Body.String(), "adopted-key") {
		t.Errorf("the response carries the api key: %s", rec.Body.String())
	}
}

// The three cases, and only the middle one offers a button.
func TestAdoptTellsTheThreePathCasesApart(t *testing.T) {
	for _, tc := range []struct {
		name      string
		libraries []jellyfin.Library
		answer    jellyfin.PathAnswer
		pathErr   error
		want      string
		says      string
	}{
		{
			name:      "a library already covers it",
			libraries: []jellyfin.Library{{Name: "Films", Locations: []string{testLibraryPath}}},
			answer:    jellyfin.PathPresent,
			want:      libraryCovered,
			says:      "nothing to add",
		},
		{
			// The one Covers exists for: a library on a parent directory takes
			// curator's path in, because Jellyfin scans recursively.
			name:      "a parent library covers it",
			libraries: []jellyfin.Library{{Name: "Everything", Locations: []string{"/media"}}},
			answer:    jellyfin.PathPresent,
			want:      libraryCovered,
			says:      "nothing to add",
		},
		{
			name:      "nothing covers it and the server can see it",
			libraries: []jellyfin.Library{{Name: "Shows", Locations: []string{"/tv"}}},
			answer:    jellyfin.PathPresent,
			want:      libraryAddable,
			says:      "can see it",
		},
		{
			// The normal case for a Jellyfin on another machine, and the one
			// curator cannot fix. Two machines do not share a disk.
			name:      "the path does not exist on that server",
			libraries: []jellyfin.Library{{Name: "Movies", Locations: []string{"/movies"}}},
			answer:    jellyfin.PathAbsent,
			want:      libraryUnseen,
			says:      "does not exist on it",
		},
		{
			// A hint that could not be taken is not a failed adoption.
			name:      "the server would not say",
			libraries: []jellyfin.Library{{Name: "Movies", Locations: []string{"/movies"}}},
			pathErr:   jellyfin.ErrUnexpectedResponse,
			want:      libraryUnknown,
			says:      "did not say",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := adopted("k")
			p.libraries, p.pathAnswer, p.pathErr = tc.libraries, tc.answer, tc.pathErr
			h, _ := setupServer(t, p, nil)

			rec := postAdopt(t, h, `{"url":"`+testAdoptURL+`","username":"a","password":"b"}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
			body := adoptedBody(t, rec)
			if body.Library.State != tc.want {
				t.Errorf("state = %q, want %q (%s)", body.Library.State, tc.want, body.Library.Detail)
			}
			if !strings.Contains(body.Library.Detail, tc.says) {
				t.Errorf("detail = %q, want it to say %q", body.Library.Detail, tc.says)
			}
			// A covered path is not looked up on disk: there is nothing left to
			// decide once a library already takes it in.
			if tc.want == libraryCovered && slices.Contains(p.calls, "PathExists") {
				t.Error("asked the server about a path a library already covers")
			}
		})
	}
}

// Declining is the default, and it has to leave the server untouched — that is
// what "opt-in per use, never a default-true flag" means when it is a test.
func TestAdoptAddsALibraryOnlyWhenAsked(t *testing.T) {
	for _, add := range []bool{false, true} {
		name := "declined"
		if add {
			name = "accepted"
		}
		t.Run(name, func(t *testing.T) {
			p := adopted("k")
			p.libraries = []jellyfin.Library{{Name: "Shows", Locations: []string{"/tv"}}}
			h, _ := setupServer(t, p, nil)

			body := `{"url":"` + testAdoptURL + `","username":"a","password":"b","add_library":false}`
			if add {
				body = `{"url":"` + testAdoptURL + `","username":"a","password":"b","add_library":true}`
			}
			rec := postAdopt(t, h, body)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}

			added := slices.Contains(p.calls, "AddLibrary")
			if added != add {
				t.Fatalf("AddLibrary ran = %v, want %v (%v)", added, add, p.calls)
			}
			report := adoptedBody(t, rec).Library
			if report.Added != add {
				t.Errorf("added = %v, want %v", report.Added, add)
			}
			if !add {
				return
			}
			if report.State != libraryCovered {
				t.Errorf("state after adding = %q, want %q", report.State, libraryCovered)
			}
			// Re-read rather than trusting the 204, for the reason AddLibrary
			// itself re-reads: a folder can come back from the listing with
			// nothing in it until a refresh materialises it.
			var reads int
			for _, call := range p.calls {
				if call == "Libraries" {
					reads++
				}
			}
			if reads != 2 {
				t.Errorf("read the listing %d times, want one before and one after the write", reads)
			}
			if !slices.Contains(report.Names, libraryName) {
				t.Errorf("names = %v, want the library that was just added", report.Names)
			}
		})
	}
}

// curator must not point a library at a path the server said it cannot see: the
// scan finds nothing and no error is produced anywhere, which is the exact
// silent failure this flow exists to remove.
func TestAdoptWillNotAddALibraryTheServerCannotSee(t *testing.T) {
	p := adopted("k")
	p.libraries = []jellyfin.Library{{Name: "Movies", Locations: []string{"/movies"}}}
	p.pathAnswer = jellyfin.PathAbsent
	h, _ := setupServer(t, p, nil)

	rec := postAdopt(t, h, `{"url":"`+testAdoptURL+`","username":"a","password":"b","add_library":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if slices.Contains(p.calls, "AddLibrary") {
		t.Error("added a library pointing at a path that server cannot see")
	}
	if got := adoptedBody(t, rec).Library.State; got != libraryUnseen {
		t.Errorf("state = %q, want %q", got, libraryUnseen)
	}
}

// The message a wrong password gets, and the message it must never get.
func TestAdoptSeparatesAWrongPasswordFromCuratorsOwnBadRequest(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		wantCode int
		says     string
		neverSay string
	}{
		{"a wrong password", jellyfin.ErrBadCredentials, http.StatusUnprocessableEntity,
			"refused that username or password", ""},
		// Measured: a missing Authorization: MediaBrowser header is 400 and a
		// wrong password is 401. Telling somebody their password is wrong when
		// curator sent a malformed header is the worst available message —
		// it is the one thing they will retype for ever.
		{"curator's own malformed request", jellyfin.ErrAuthRequestRejected, http.StatusBadGateway,
			"not your password", "username or password"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := adopted("k")
			p.failAt, p.failWith = "Authenticate", tc.err
			h, writer := setupServer(t, p, nil)

			rec := postAdopt(t, h, `{"url":"`+testAdoptURL+`","username":"a","password":"b"}`)
			if rec.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.wantCode, rec.Body.String())
			}
			body := failureBody(t, rec)
			said := body.Error + "\n" + strings.Join(body.Instructions, "\n")
			if !strings.Contains(said, tc.says) {
				t.Errorf("said %q, want it to say %q", said, tc.says)
			}
			if tc.neverSay != "" && strings.Contains(body.Error, tc.neverSay) {
				t.Errorf("told the user %q when the request was curator's fault: %q", tc.neverSay, body.Error)
			}
			if len(body.Instructions) == 0 {
				t.Error("no instructions: every failure ends somewhere the user can still act")
			}
			if writer.calls != 0 {
				t.Errorf("recorded a failed adoption %d times", writer.calls)
			}
			if slices.Contains(p.calls, "MintKey") {
				t.Error("minted a key on a server that refused the sign-in")
			}
		})
	}
}

// A key that authenticates and cannot read /Items is a working setup screen and
// a broken Open in Jellyfin link, and nothing else in this flow can see it.
func TestAdoptFailsWhenTheMintedKeyCannotReadTheLibrary(t *testing.T) {
	h, writer, _ := setupServerWithReader(t, adopted("k"), nil,
		&fakeReader{err: fmt.Errorf("checking: %w", jellyfin.ErrUnauthorized)})

	rec := postAdopt(t, h, `{"url":"`+testAdoptURL+`","username":"a","password":"b"}`)
	if rec.Code == http.StatusOK {
		t.Fatalf("said done with a key that cannot read the library: %s", rec.Body.String())
	}
	if writer.calls != 0 {
		t.Errorf("recorded a key it could not read with, %d times", writer.calls)
	}
	if body := failureBody(t, rec); body.Step != "checking the key" {
		t.Errorf("step = %q, want the check", body.Step)
	}
}

// A film that is not there is not a failure. A Jellyfin on a NAS that has never
// seen curator's disk is a correct adoption and an empty answer.
func TestAdoptSucceedsWhenTheFilmIsNotOnThatServer(t *testing.T) {
	h, writer, _ := setupServerWithReader(t, adopted("k"), nil, &fakeReader{err: jellyfin.ErrNotFound})

	rec := postAdopt(t, h, `{"url":"`+testAdoptURL+`","username":"a","password":"b"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if body := adoptedBody(t, rec); body.Check.Found {
		t.Error("reported the film as found when jellyfin said it had none")
	}
	if writer.calls != 1 {
		t.Errorf("WriteSettings called %d times, want 1", writer.calls)
	}
}

// The branch is chosen by the server, not by the user — including when the
// screen's probe was a moment out of date.
func TestAdoptRefusesAServerThatHasNeverBeenSetUp(t *testing.T) {
	p := &fakeProvisioner{key: "k", status: jellyfin.ServerStatus{Version: "10.10.7"}}
	h, writer := setupServer(t, p, nil)

	rec := postAdopt(t, h, `{"url":"`+testAdoptURL+`","username":"a","password":"b"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if len(p.calls) != 0 {
		t.Errorf("signed in to a server with no accounts on it yet: %v", p.calls)
	}
	if writer.calls != 0 {
		t.Errorf("WriteSettings called %d times", writer.calls)
	}
}

func TestAdoptRefusesAnUnusableAddress(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"no url at all", `{"username":"a","password":"b"}`},
		{"a blank url", `{"url":"  ","username":"a","password":"b"}`},
		{"no scheme", `{"url":"192.168.1.26:8096","username":"a","password":"b"}`},
		{"a scheme that is not http", `{"url":"file:///etc/passwd","username":"a","password":"b"}`},
		{"no host", `{"url":"http://","username":"a","password":"b"}`},
		{"no password", `{"url":"` + testAdoptURL + `","username":"a"}`},
		{"not an object", `"nope"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := adopted("k")
			h, _ := setupServer(t, p, nil)
			if rec := postAdopt(t, h, tc.body); rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
			if len(p.calls) != 0 {
				t.Errorf("reached out to something: %v", p.calls)
			}
		})
	}
}

// Jellyfin's own UI shows people a URL with /web/index.html on the end, and
// pasting that is the single most likely thing to happen on this screen. Every
// path is built from the base, so a kept one would 404 the whole flow.
func TestAdoptTakesTheBaseOfAPastedWebURL(t *testing.T) {
	h, writer, reader := setupServerWithReader(t, adopted("k"), nil, &fakeReader{err: jellyfin.ErrNotFound})

	rec := postAdopt(t, h,
		`{"url":"http://192.168.1.26:8096/web/index.html#/home.html","username":"a","password":"b"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if got := writer.set["jellyfin_url"]; got != testAdoptURL {
		t.Errorf("wrote jellyfin_url = %q, want the base %q", got, testAdoptURL)
	}
	if reader.baseURL != testAdoptURL {
		t.Errorf("checked at %q, want the base", reader.baseURL)
	}
}

// D28's sharp edge again, on the branch where the server is somebody else's:
// found out before their Jellyfin is touched at all.
func TestAdoptRefusesWhenTheEnvironmentOwnsTheSettings(t *testing.T) {
	p := adopted("k")
	h, writer := setupServer(t, p, map[string]SettingState{
		"jellyfin_url": {Source: settings.SourceEnv, Configured: true},
	})

	rec := postAdopt(t, h, `{"url":"`+testAdoptURL+`","username":"a","password":"b"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if len(p.calls) != 0 {
		t.Errorf("touched somebody's jellyfin before finding out it could not record the result: %v", p.calls)
	}
	if writer.calls != 0 {
		t.Errorf("WriteSettings called %d times", writer.calls)
	}
}

// The probe is what the adopt form asks before it asks for a password, so it has
// to be able to ask about an address curator did not choose.
func TestProbeAnswersAboutATypedAddress(t *testing.T) {
	var asked string
	writer := &fakeWriter{}
	set := fullSettings()
	set.Writer = writer
	mux := http.NewServeMux()
	New(newFakeStore(), ScannerFunc(nil), nil, fixtureRoot, quiet()).
		WithSettings(set).
		WithJellyfinSetup(JellyfinSetup{
			URL:         testJellyfinURL,
			LibraryPath: testLibraryPath,
			New: func(baseURL string) Provisioner {
				asked = baseURL
				return adopted("k")
			},
		}).
		RegisterJellyfin(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/jellyfin/probe?url=http%3A%2F%2F192.168.1.26%3A8096%2Fweb%2F", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if asked != testAdoptURL {
		t.Errorf("probed %q, want the base of the typed address", asked)
	}
	var body jellyfinProbeBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("probe body: %v", err)
	}
	if body.URL != testAdoptURL {
		t.Errorf("url = %q, want the address that was probed", body.URL)
	}
	if body.State != probeConfigured {
		t.Errorf("state = %q, want %q", body.State, probeConfigured)
	}

	// A bad one is refused rather than fetched. Without this the probe is a
	// fetch-anything-curator-can-reach primitive on a product that ships with
	// authentication off (D25).
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/jellyfin/probe?url=file%3A%2F%2F%2Fetc%2Fpasswd", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a scheme that is not http", rec.Code)
	}
}

// What a stranger's server answered is not echoed back. internal/jellyfin quotes
// up to 256 bytes of a response in its error, which is right for the Jellyfin
// curator was pointed at and is a small read-anything primitive when the address
// came from whoever opened the page.
func TestProbeDoesNotEchoWhatATypedAddressAnswered(t *testing.T) {
	const secret = "SECRET-INTERNAL-PAGE-CONTENTS"
	p := &fakeProvisioner{statusErr: fmt.Errorf("answered 200 (%q): %w", secret, jellyfin.ErrUnexpectedResponse)}
	h, _ := setupServer(t, p, nil)

	// curator's own configured address: the detail is for debugging and is kept.
	if _, body := probe(t, h); !strings.Contains(body.Detail, secret) {
		t.Errorf("detail = %q, want the real error for curator's own address", body.Detail)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/jellyfin/probe?url=http%3A%2F%2F10.0.0.9%3A8080", nil))
	var body jellyfinProbeBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("probe body: %v", err)
	}
	if strings.Contains(body.Detail, secret) {
		t.Errorf("echoed what another host answered: %q", body.Detail)
	}
	if body.State != probeUnreachable {
		t.Errorf("state = %q, want %q", body.State, probeUnreachable)
	}
}

// curator's own 401 means "you are not signed in to curator", and the browser
// client turns one into the login gate. No branch of this endpoint may answer
// one, or a stranger provisioning a Jellyfin gets thrown back to curator's
// password box because a different server refused something.
func TestProvisionNeverAnswers401(t *testing.T) {
	for _, err := range []error{
		jellyfin.ErrBadCredentials,
		jellyfin.ErrAuthRequestRejected,
		jellyfin.ErrUnauthorized,
		jellyfin.ErrAlreadyConfigured,
		jellyfin.ErrUnexpectedResponse,
		jellyfin.ErrUnreachable,
		jellyfin.ErrNotReady,
	} {
		for _, at := range []string{"Configure", "Authenticate", "MintKey", "CompleteSetup"} {
			h, _ := setupServer(t, &fakeProvisioner{key: "k", failAt: at, failWith: err}, nil)
			rec := postProvision(t, h, `{"username":"a","password":"b"}`)
			if rec.Code == http.StatusUnauthorized {
				t.Errorf("%v at %s answered 401, which the browser turns into curator's login gate", err, at)
			}
		}

		// The adopt branch is where a genuinely wrong password arrives, so it is
		// the one most likely to be given a 401 by somebody being helpful.
		for _, at := range []string{"Authenticate", "MintKey", "Libraries", "PathExists", "AddLibrary"} {
			p := adopted("k")
			p.failAt, p.failWith = at, err
			h, _ := setupServer(t, p, nil)
			rec := postAdopt(t, h, `{"url":"`+testAdoptURL+`","username":"a","password":"b","add_library":true}`)
			if rec.Code == http.StatusUnauthorized {
				t.Errorf("adopt: %v at %s answered 401, which the browser turns into curator's login gate", err, at)
			}
			if len(failureBody(t, rec).Instructions) == 0 && rec.Code != http.StatusOK {
				t.Errorf("adopt: %v at %s failed with no instructions", err, at)
			}
		}
	}
}
