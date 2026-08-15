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
	if consent != jellyfin.OnlyIfUnconfigured {
		return fmt.Errorf("AddLibrary got consent %v; the setup flow may never adopt", consent)
	}
	_ = name
	return f.record("AddLibrary")
}

func (f *fakeProvisioner) MintKey(
	_ context.Context, session jellyfin.Session, app string, consent jellyfin.Consent,
) (string, error) {
	if session.Token == "" {
		return "", errors.New("MintKey ran without a session")
	}
	if consent != jellyfin.OnlyIfUnconfigured {
		return "", fmt.Errorf("MintKey got consent %v; the setup flow may never adopt", consent)
	}
	_ = app
	if err := f.record("MintKey"); err != nil {
		return "", err
	}
	return f.key, nil
}

func (f *fakeProvisioner) CompleteSetup(context.Context) error {
	return f.record("CompleteSetup")
}

// setupServer wires the Playback screen over a fake Provisioner and a fake
// settings writer, exactly as cmd/curator wires the real ones.
func setupServer(t *testing.T, p *fakeProvisioner, states map[string]SettingState) (http.Handler, *fakeWriter) {
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
		}).
		RegisterJellyfin(mux)
	return mux, writer
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
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, strings.NewReader("{}")))
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d, want 503", tc.method, tc.path, rec.Code)
		}
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
	}
}
