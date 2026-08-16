package api

// The Playback screen's three endpoints: find out what is at the other end, set
// it up, and connect to one somebody already runs
// (docs/tasks/T65-playback-screen.md, docs/tasks/T66-adopt-jellyfin.md).
//
// Provisioning is not a setting write, which is why it is here and not in
// settings.go: it is eight calls to another server with failure modes of its
// own, and it *ends* by writing settings through the machinery phase 7 already
// built rather than beside it.
//
// The two writing flows are deliberately separate handlers and not one with a
// mode flag. They differ in the thing that matters most: provision runs against
// a server curator started and may touch its wizard, adopt runs against one a
// household is watching and may not touch anything except additively. A shared
// handler would put those two on either side of one `if`, which is precisely
// the shape D34 exists to prevent.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/DulsaraNethmin/curator/internal/config"
	"github.com/DulsaraNethmin/curator/internal/jellyfin"
	"github.com/DulsaraNethmin/curator/internal/settings"
)

// provisionTimeout bounds the whole sequence.
//
// Far longer than probeTimeout because this is not a reachability check: it is
// eight requests, one of which asks Jellyfin to create a library and scan it.
// It is a ceiling for a server that has stopped answering mid-way, not a
// budget — every individual call has internal/jellyfin's own 15 s on it.
const provisionTimeout = 90 * time.Second

// libraryName is what curator's Movies library is called inside Jellyfin.
//
// A constant rather than a field on the screen for the same reason the path is
// not a field: there is one library, curator creates it, and a name somebody
// can type is a name that can disagree with the one the next version looks for.
const libraryName = "Movies"

// Provisioner is the writing half of internal/jellyfin, as this package needs
// it.
//
// Declared here rather than taking *jellyfin.Provisioner so the handler can be
// exercised against a fake — and, more to the point, so that the only thing
// able to reach these methods is the setup flow that was handed one. The
// importer and the poller hold a MediaServer, which has FindMovie and nothing
// else (docs/decisions.md D34).
type Provisioner interface {
	Status(ctx context.Context) (jellyfin.ServerStatus, error)
	Configure(ctx context.Context, want jellyfin.StartupConfiguration) error
	CreateAdmin(ctx context.Context, name, password string) error
	EnableRemoteAccess(ctx context.Context) error
	Authenticate(ctx context.Context, username, password string) (jellyfin.Session, error)
	AddLibrary(ctx context.Context, session jellyfin.Session, name, libraryPath string, consent jellyfin.Consent) error
	MintKey(ctx context.Context, session jellyfin.Session, app string, consent jellyfin.Consent) (string, error)
	CompleteSetup(ctx context.Context) error

	// The adopt branch's two reads. Both are reads, both take no Consent, and
	// neither may change anything about a server somebody else configured.
	Libraries(ctx context.Context, session jellyfin.Session) ([]jellyfin.Library, error)
	PathExists(ctx context.Context, session jellyfin.Session, path string) (jellyfin.PathAnswer, error)
}

// JellyfinSetup is what the Playback screen needs behind it.
type JellyfinSetup struct {
	// URL is where the setup flow looks for a Jellyfin. Inside the bundle that
	// is the compose service name, because compose deliberately sets no
	// JELLYFIN_URL — an environment value would beat anything provisioning
	// writes (docs/decisions.md D28), so cmd/curator resolves this once and the
	// handler never guesses.
	URL string

	// LibraryPath is LIBRARY_MOVIES as this process resolved it, which is the
	// path Jellyfin gets pointed at. It is not a field on the screen and must
	// not become one: two services disagreeing about a path is the number-one
	// silent failure in self-hosted media, and a text input is how it gets
	// reintroduced.
	LibraryPath string

	// New builds a Provisioner for a base URL. A function, so cmd/curator owns
	// the HTTP client and the version string, and so a test can hand back a
	// fake without a listening socket.
	New func(baseURL string) Provisioner

	// Reader builds curator's OWN read-only client for a base URL and a key —
	// the same jellyfin.Client the movie screen draws Open in Jellyfin with.
	//
	// It exists so adoption can finish by proving the key it just minted with
	// **the call that will actually use it**, rather than with another of the
	// Provisioner's. A key that authenticates but cannot read /Items is a setup
	// screen that says "done" and an Open in Jellyfin link that never appears,
	// and nothing else in this flow can tell those apart. Nil disables the check
	// rather than failing the adoption, which is the state every test that does
	// not care about it is in.
	Reader func(baseURL, apiKey string) MediaServer
}

// WithJellyfinSetup attaches the Playback screen's dependencies.
//
// Nil New leaves the endpoints answering 503, which is the honest state for a
// process that has no way to reach a Jellyfin.
func (s *Server) WithJellyfinSetup(setup JellyfinSetup) *Server {
	s.jellyfinSetup = &setup
	return s
}

// RegisterJellyfin mounts the Playback screen's routes.
func (s *Server) RegisterJellyfin(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/jellyfin/probe", s.handleJellyfinProbe)
	mux.HandleFunc("POST /api/jellyfin/provision", s.handleJellyfinProvision)
	mux.HandleFunc("POST /api/jellyfin/adopt", s.handleJellyfinAdopt)
}

// ready reports the setup dependencies, or answers the honest 503 for a process
// that has no way to reach a Jellyfin at all.
func (s *Server) ready(w http.ResponseWriter, needWriter bool) (*JellyfinSetup, bool) {
	setup := s.jellyfinSetup
	if setup == nil || setup.New == nil {
		s.fail(w, http.StatusServiceUnavailable,
			errors.New("this process cannot set up a jellyfin"))
		return nil, false
	}
	if needWriter && (s.settings == nil || s.settings.Writer == nil) {
		// Refused before anything is touched, and this ordering is the point: a
		// curator that set a Jellyfin up and then could not record the key would
		// leave the user with a configured media server curator cannot talk to,
		// and no way back short of deleting a volume.
		s.fail(w, http.StatusServiceUnavailable,
			errors.New("this process cannot write settings, so it will not set up a jellyfin it cannot record"))
		return nil, false
	}
	return setup, true
}

// The four worlds the probe distinguishes.
//
// T65 branches on three — unreachable, set-up-able, already configured — and
// this reports four, because "nothing is listening" and "it is up and still
// loading" are the same instruction to the screen and different sentences to
// put on it. Nothing listening means the pasted command did not run; still
// loading means it did, and the difference is the whole of what somebody
// staring at a spinner wants to know.
const (
	probeUnreachable = "unreachable"
	probeStarting    = "starting"
	probeNeedsSetup  = "needs_setup"
	probeConfigured  = "configured"
)

// jellyfinProbeBody is what GET /api/jellyfin/probe reports.
type jellyfinProbeBody struct {
	State string `json:"state"`
	URL   string `json:"url"`

	// Version is only ever present when something answered.
	Version string `json:"version,omitempty"`

	// LibraryPath is what provisioning will hand Jellyfin, sent with the probe
	// so the screen can show it read-only without a second round trip and
	// without a copy of the path in the UI.
	LibraryPath string `json:"library_path"`

	// Command is the line the user pastes. It is here rather than hard-coded in
	// the UI because it names the profile in compose.yaml, and two spellings of
	// a profile name is a service that never starts and never errors.
	Command string `json:"command"`

	Detail string `json:"detail,omitempty"`
}

// bundleCommand is the one line phase 9 accepts as the cost of refusing the
// Docker socket (docs/decisions.md D34). The profile is compose.yaml's.
const bundleCommand = "docker compose --profile jellyfin up -d"

// handleJellyfinProbe reports which of the four worlds the Jellyfin is in.
//
// Short-timeout and read-only: /System/Info/Public needs no credential even on
// a configured server, which is what makes it usable both as a probe and as
// the Provisioner's own guard. The screen polls this while a container starts,
// so it must never be the thing that hangs.
func (s *Server) handleJellyfinProbe(w http.ResponseWriter, r *http.Request) {
	setup, ok := s.ready(w, false)
	if !ok {
		return
	}

	// ?url= is T66's branch. The adopt flow asks for an address before it asks
	// for anything else, because an unreachable one is a typo or a firewall and
	// finding that out after somebody has typed a password is a worse experience
	// for no reason.
	target, typed, err := probeTarget(setup, r.URL.Query().Get("url"))
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
	defer cancel()

	body := jellyfinProbeBody{
		URL:         target,
		LibraryPath: setup.LibraryPath,
		Command:     bundleCommand,
	}

	status, err := setup.New(target).Status(ctx)
	switch {
	case err == nil:
		body.Version = status.Version
		if status.WizardCompleted {
			body.State = probeConfigured
			body.Detail = "this jellyfin has already been set up, so curator will not run its wizard again"
		} else {
			body.State = probeNeedsSetup
			body.Detail = "this jellyfin has never been set up"
		}
	case errors.Is(err, jellyfin.ErrNotReady):
		body.State = probeStarting
		body.Detail = "jellyfin is up and still loading"
	case errors.Is(err, jellyfin.ErrUnreachable):
		body.State = probeUnreachable
		body.Detail = "nothing answered"
	default:
		// Something answered and it was not a Jellyfin this version
		// understands. Reported as unreachable rather than as a 500, because
		// the screen's next move is the same — keep waiting, or fall back to
		// the manual steps — and a settings page that 500s while a container
		// boots is a page people reload instead of read.
		body.State = probeUnreachable
		body.Detail = strangerDetail(err, typed, target)
	}

	// Always 200. The states above are the answer, not the failure: a probe
	// that 503s when nothing is listening makes "the container is not up yet",
	// which is the expected state for most of this screen's life, look
	// identical to "curator is broken" in a browser's network tab.
	s.respond(w, http.StatusOK, body)
}

// jellyfinProvisionRequest is what the Playback screen posts.
type jellyfinProvisionRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`

	// PublicURL is what a browser and an Apple TV reach Jellyfin at, and the
	// browser is the only thing that knows it: curator runs in a container and
	// cannot learn its host's LAN address. Empty is accepted and means "leave
	// it unset, the browser reaches Jellyfin where curator does" — which is
	// right on a laptop and wrong in the bundle, so the screen always sends
	// one rather than relying on this.
	PublicURL string `json:"public_url"`
}

// jellyfinProvisionBody is what a successful provision reports.
//
// No key, not even truncated. It went straight into phase 7's secret machinery
// and the only fact this endpoint may report about it is that it exists
// (docs/decisions.md D17, D28).
type jellyfinProvisionBody struct {
	Username    string `json:"username"`
	URL         string `json:"url"`
	PublicURL   string `json:"public_url"`
	LibraryPath string `json:"library_path"`
	Library     string `json:"library"`
}

// jellyfinFailureBody is a provisioning failure the screen can act on.
type jellyfinFailureBody struct {
	Error string `json:"error"`

	// Step is which of the sequence failed, so the screen can say "curator got
	// as far as X" rather than "something went wrong".
	Step string `json:"step"`

	// Adopt says this server is already set up and the manual route is not the
	// only one left — T66's branch is. It is a separate flag rather than the
	// screen matching on a message, because a message is prose and this is a
	// branch.
	Adopt bool `json:"adopt,omitempty"`

	// Instructions is the fallback, and it is required rather than polish. The
	// startup endpoints are not a documented contract, so every failure has to
	// end somewhere the user can still get to a working Jellyfin — with the
	// real library path in it, because that is the step people get wrong.
	Instructions []string `json:"instructions,omitempty"`
}

// handleJellyfinProvision runs the whole wizard and records the result.
func (s *Server) handleJellyfinProvision(w http.ResponseWriter, r *http.Request) {
	setup, ok := s.ready(w, true)
	if !ok {
		return
	}

	var req jellyfinProvisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.fail(w, http.StatusBadRequest, errors.New("body is not a JSON object"))
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	req.PublicURL = strings.TrimSpace(req.PublicURL)
	if req.Username == "" || req.Password == "" {
		s.fail(w, http.StatusBadRequest,
			errors.New("username and password are the credentials you will sign in to jellyfin with; both are required"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), provisionTimeout)
	defer cancel()

	// Every key this is about to write, checked for an environment owner
	// BEFORE Jellyfin is touched, for the same reason the Writer is: the
	// environment beats the store, so writing a shadowed key would report
	// success and change nothing anybody can see (docs/decisions.md D28). The
	// settings PUT already answers 409 for this; so does this.
	if shadowed, err := s.shadowedByEnv(ctx, req.PublicURL != ""); err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	} else if len(shadowed) > 0 {
		s.respond(w, http.StatusConflict, shadowedBody(shadowed))
		return
	}

	provisioner := setup.New(setup.URL)
	key, err := s.provision(ctx, provisioner, setup, req)
	if err != nil {
		status, body := s.provisionFailure(err, setup)
		s.respond(w, status, body)
		return
	}

	// Written last, in one transaction, through the machinery phase 7 built.
	// The key is a secret in the registry, so it is encrypted on the far side
	// of this interface and never read back across the API.
	set := map[string]string{
		"jellyfin_url":     setup.URL,
		"jellyfin_api_key": key,
		"playback_target":  config.PlaybackJellyfin,
	}
	if req.PublicURL != "" {
		set["jellyfin_public_url"] = req.PublicURL
	}
	if err := s.settings.Writer.WriteSettings(ctx, set, nil); err != nil {
		// Jellyfin is set up and curator could not write it down. Say exactly
		// that, because the recovery is to paste the key by hand and the user
		// cannot know that from "internal error".
		s.respond(w, http.StatusInternalServerError, jellyfinFailureBody{
			Step:  "record",
			Error: "jellyfin was set up, but curator could not save the result: " + err.Error(),
			Instructions: []string{
				"Jellyfin is configured and your sign-in works — nothing needs doing there.",
				"In Jellyfin: Dashboard → API Keys, and copy the key named " + jellyfin.KeyAppName + ".",
				"Paste it into this page's Jellyfin section as JELLYFIN_API_KEY, with JELLYFIN_URL set to " + setup.URL + ".",
			},
		})
		return
	}

	s.log.Info("jellyfin provisioned",
		"url", setup.URL, "library", setup.LibraryPath, "user", req.Username)

	s.respond(w, http.StatusOK, jellyfinProvisionBody{
		Username:    req.Username,
		URL:         setup.URL,
		PublicURL:   req.PublicURL,
		LibraryPath: setup.LibraryPath,
		Library:     libraryName,
	})
}

// provisionStep names the step for the error body, and is the string the screen
// puts after "curator got as far as".
type provisionStep struct {
	name string
	run  func() error
}

// provision runs the measured sequence and returns the minted key.
//
// The order is T64's and it is not arbitrary: CompleteSetup is last, so that a
// failure anywhere above it leaves the server visibly unfinished rather than
// finished and wrong. Authenticating, creating the library and minting the key
// all work before the wizard is closed — measured — which is what makes that
// ordering available at all.
func (s *Server) provision(
	ctx context.Context, p Provisioner, setup *JellyfinSetup, req jellyfinProvisionRequest,
) (string, error) {
	var (
		session jellyfin.Session
		key     string
	)

	steps := []provisionStep{
		{"locale", func() error {
			// The three the wizard's first screen sets. Left as Jellyfin's own
			// answer rather than curator's opinion: Configure reads before it
			// writes, so an empty field here keeps what the server already had.
			return p.Configure(ctx, jellyfin.StartupConfiguration{})
		}},
		{"the administrator", func() error {
			return p.CreateAdmin(ctx, req.Username, req.Password)
		}},
		{"remote access", func() error {
			return p.EnableRemoteAccess(ctx)
		}},
		{"signing in", func() error {
			var err error
			session, err = p.Authenticate(ctx, req.Username, req.Password)
			return err
		}},
		{"the library", func() error {
			return p.AddLibrary(ctx, session, libraryName, setup.LibraryPath, jellyfin.OnlyIfUnconfigured)
		}},
		{"the API key", func() error {
			var err error
			key, err = p.MintKey(ctx, session, jellyfin.KeyAppName, jellyfin.OnlyIfUnconfigured)
			return err
		}},
		{"finishing the wizard", func() error {
			return p.CompleteSetup(ctx)
		}},
	}

	for _, step := range steps {
		if err := step.run(); err != nil {
			return "", stepError{step: step.name, err: err}
		}
	}
	if key == "" {
		return "", stepError{
			step: "the API key",
			err: fmt.Errorf("jellyfin returned an empty api key: %w", jellyfin.ErrUnexpectedResponse),
		}
	}
	return key, nil
}

// stepError carries which step failed alongside the error, so the handler can
// name it without a second switch that has to agree with the first.
type stepError struct {
	step string
	err  error
}

func (e stepError) Error() string { return e.step + ": " + e.err.Error() }
func (e stepError) Unwrap() error { return e.err }

// provisionFailure turns a sequence error into the status and the body.
//
// Every branch carries instructions. That is the phase requirement rather than
// politeness: the startup endpoints are what the wizard happens to call in the
// version we pin, so a flow that cannot survive a Jellyfin answering something
// new is a support burden this project cannot carry.
func (s *Server) provisionFailure(err error, setup *JellyfinSetup) (int, jellyfinFailureBody) {
	body := jellyfinFailureBody{Error: err.Error()}
	var step stepError
	if errors.As(err, &step) {
		body.Step = step.step
		body.Error = step.err.Error()
	}

	status := http.StatusBadGateway
	switch {
	case errors.Is(err, jellyfin.ErrAlreadyConfigured):
		// Not a failure of curator's and not one manual steps fix: this server
		// is already somebody's, and adding to it is T66's branch.
		status = http.StatusConflict
		body.Adopt = true
		body.Instructions = []string{
			"This Jellyfin has already been set up, so curator will not touch its wizard — that is the guard that stops a household being locked out of a server they are watching.",
			"Connect to it instead: sign in with an existing Jellyfin account and curator will mint its own key.",
		}
		return status, body

	case errors.Is(err, jellyfin.ErrUnreachable):
		status = http.StatusServiceUnavailable
		body.Instructions = []string{
			"Nothing answered at " + setup.URL + ".",
			"Run " + bundleCommand + " in the directory holding compose.yaml, and wait for it to finish starting.",
		}
		return status, body

	case errors.Is(err, jellyfin.ErrNotReady):
		status = http.StatusServiceUnavailable
		body.Instructions = []string{
			"Jellyfin is up and still loading. Wait a few seconds and try again — a cold start took 14 to 27 seconds when this was measured.",
		}
		return status, body

	case errors.Is(err, jellyfin.ErrBadCredentials):
		// 422 and deliberately NOT 401. curator's own 401 means "you are not
		// signed in to curator", and the browser client turns one into the
		// login gate — so answering 401 here would throw a stranger back to
		// curator's password box because *Jellyfin* refused something.
		//
		// And in this flow it is not the user's typo to fix, which is the other
		// reason it needs its own sentence: the credentials being refused are
		// the ones curator created two steps earlier. Reaching here means
		// Jellyfin accepted the account and then would not accept a sign-in
		// with it, which is a server behaving unexpectedly rather than a
		// password to retype. T66's adopt branch is where a genuinely wrong
		// password arrives.
		status = http.StatusUnprocessableEntity
		body.Error = "jellyfin refused a sign-in with the account curator had just created: " + body.Error
		body.Instructions = manualSteps(setup)
		return status, body
	}

	body.Instructions = manualSteps(setup)
	return status, body
}

// manualSteps is the fallback every failure ends at: how to get a working
// Jellyfin by hand, with the real path in it.
//
// The path is the whole reason this list is generated rather than written into
// the UI. It is what this process resolved LIBRARY_MOVIES to, so it is correct
// for a bundle, a bind mount and a laptop alike — and the library path is
// precisely the step people get wrong when they do this by hand.
func manualSteps(setup *JellyfinSetup) []string {
	return []string{
		"Open Jellyfin yourself and run its own setup wizard.",
		"When it asks for a library, add a Movies library at exactly this path: " + setup.LibraryPath,
		"Then in Jellyfin: Dashboard → API Keys → new key, and paste it into this page's Jellyfin section as JELLYFIN_API_KEY.",
		"Set JELLYFIN_URL to " + setup.URL + " in the same section.",
	}
}

// shadowedByEnv returns the environment variables that own the settings this
// flow is about to write.
//
// Only the keys it will actually write: an env-owned jellyfin_public_url does
// not matter when the browser sent nothing to put in it.
func (s *Server) shadowedByEnv(ctx context.Context, withPublicURL bool) ([]string, error) {
	states, err := s.settings.states(ctx)
	if err != nil {
		return nil, err
	}

	keys := []string{"jellyfin_url", "jellyfin_api_key", "playback_target"}
	if withPublicURL {
		keys = append(keys, "jellyfin_public_url")
	}

	var shadowed []string
	for _, key := range keys {
		if states[key].Source != settings.SourceEnv {
			continue
		}
		item, ok := settings.Get(key)
		if !ok {
			continue
		}
		shadowed = append(shadowed, item.Env)
	}
	return shadowed, nil
}

// shadowedBody is the 409 both writing flows answer when the environment owns a
// key they were about to write.
func shadowedBody(shadowed []string) jellyfinFailureBody {
	return jellyfinFailureBody{
		Step: "record",
		Error: fmt.Sprintf(
			"%s set in the environment, which beats anything saved — curator would set jellyfin up and then be unable to record it",
			strings.Join(shadowed, " and ")),
		Instructions: []string{
			"Unset " + strings.Join(shadowed, " and ") + " where curator is started, then try again.",
			"In the bundle that means removing it from compose.yaml's environment: curator ships without it on purpose.",
		},
	}
}

// --- T66: connecting to a Jellyfin somebody already runs --------------------

// probeTarget resolves which Jellyfin a request is asking about, and says
// whether the caller typed it.
//
// Empty is the bundle's, which is every request T65's screen makes. A ?url= is
// the adopt branch: an address curator did not choose and cannot validate
// against anything, which is exactly why it is parsed here rather than handed
// to an http.Client as a string.
//
// The scheme check is the guard. Without it this endpoint is a fetch anything
// curator can reach primitive on a product that ships with authentication off
// by default (docs/decisions.md D25), and a wrong scheme is the cheap half of
// closing that: what remains is a reachability oracle for http hosts on the
// LAN, which is what any browser on the same LAN already is.
func probeTarget(setup *JellyfinSetup, raw string) (string, bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return setup.URL, false, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", true, errors.New("that is not an address curator can read")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", true, errors.New(
			"a jellyfin address has to start with http:// or https:// — for example http://192.168.1.26:8096")
	}
	if parsed.Host == "" {
		return "", true, errors.New("that address has no host in it — for example http://192.168.1.26:8096")
	}
	// Everything after the host is dropped rather than rejected. Jellyfin's own
	// UI shows people a URL with /web/index.html on the end of it, and pasting
	// that is the single most likely thing to happen here; every path below is
	// built from the base, so keeping one would 404 the whole flow.
	return parsed.Scheme + "://" + parsed.Host, true, nil
}

// strangerDetail is what the screen is told about a server that answered
// something curator does not understand.
//
// The body is echoed for curator's OWN configured address and never for a typed
// one. internal/jellyfin quotes up to 256 bytes of a response in its error, which
// is exactly right for debugging the Jellyfin curator was pointed at and is a
// small read-anything primitive when the address came from whoever opened the
// page.
func strangerDetail(err error, typed bool, target string) string {
	if !typed {
		return err.Error()
	}
	return "something answered at " + target + " and it was not a jellyfin curator recognises"
}

// The three cases T66 renders for curator's library path, and the screen offers
// a button on exactly one of them.
const (
	// libraryCovered — a library already takes curator's path in. Nothing to do.
	libraryCovered = "covered"
	// libraryAddable — no library covers it and the server can see it. Offer.
	libraryAddable = "addable"
	// libraryUnseen — the server says that path does not exist on it, which is
	// the normal case for a Jellyfin on another machine. curator cannot fix it
	// and must not add a library pointing at a path that server cannot see.
	libraryUnseen = "unseen"
	// libraryUnknown — curator asked and got no answer it will act on.
	libraryUnknown = "unknown"
)

// jellyfinAdoptRequest is what the adopt form posts.
type jellyfinAdoptRequest struct {
	// URL is typed, because curator did not start this server and has no way to
	// know where it is.
	URL       string `json:"url"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	PublicURL string `json:"public_url"`

	// AddLibrary is the explicit opt-in for the one write this branch performs.
	//
	// It is on the request rather than decided here, it is never defaulted true,
	// and the screen never pre-ticks it: a library is the only thing curator may
	// add to a server somebody else configured, and "may" is not "should".
	AddLibrary bool `json:"add_library"`
}

// jellyfinLibraryReport is what curator found out about its own path on a server
// it does not own.
type jellyfinLibraryReport struct {
	State  string `json:"state"`
	Detail string `json:"detail"`

	// Names is every library the server holds. All of them, because a real
	// server has Movies, Shows, Music and a folder somebody made in 2019, and a
	// screen that showed one would be describing a server nobody runs.
	Names []string `json:"names"`

	// Added says this request created a library. Only ever true when the caller
	// asked for one.
	Added bool `json:"added"`
}

// jellyfinCheckReport is curator's own call, with the key curator just minted.
type jellyfinCheckReport struct {
	// Film is what was looked for, empty when curator's library holds nothing
	// TMDB has matched — in which case the query still ran and still proves the
	// key can read /Items, which is the half that can fail.
	Film string `json:"film,omitempty"`

	// Found is information and not a verdict. A Jellyfin on a NAS that has never
	// seen curator's disk answers false and is still correctly adopted.
	Found  bool   `json:"found"`
	Detail string `json:"detail"`
}

// jellyfinAdoptedBody is what a successful adoption reports. No key, for the
// same reason provisioning sends none.
type jellyfinAdoptedBody struct {
	Username    string                `json:"username"`
	URL         string                `json:"url"`
	PublicURL   string                `json:"public_url"`
	LibraryPath string                `json:"library_path"`
	Version     string                `json:"version"`
	Library     jellyfinLibraryReport `json:"library"`
	Check       jellyfinCheckReport   `json:"check"`
}

// handleJellyfinAdopt connects curator to a Jellyfin that is already somebody's.
//
// The whole shape of this handler is the promise that it changes nothing. It
// authenticates, mints a key, reads two things, and — only when asked outright —
// adds one library. Nothing under /Startup/ is reachable from here at all:
// Provisioner refuses every one of those against a configured server, which is
// the guard that earns its keep on exactly this branch (docs/decisions.md D34).
func (s *Server) handleJellyfinAdopt(w http.ResponseWriter, r *http.Request) {
	setup, ok := s.ready(w, true)
	if !ok {
		return
	}

	var req jellyfinAdoptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.fail(w, http.StatusBadRequest, errors.New("body is not a JSON object"))
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	req.PublicURL = strings.TrimSpace(req.PublicURL)
	if req.Username == "" || req.Password == "" {
		s.fail(w, http.StatusBadRequest,
			errors.New("sign in with an account that already exists on that jellyfin; both a username and a password are required"))
		return
	}
	target, _, err := probeTarget(setup, req.URL)
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.URL) == "" {
		// Not a default worth having. Adoption writes jellyfin_url, and writing
		// the bundle's service name for a server the user reached some other way
		// is how Open in Jellyfin ends up pointing at a host that does not exist.
		s.fail(w, http.StatusBadRequest,
			errors.New("which jellyfin? give the address you open it at, for example http://192.168.1.26:8096"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), provisionTimeout)
	defer cancel()

	if shadowed, err := s.shadowedByEnv(ctx, req.PublicURL != ""); err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	} else if len(shadowed) > 0 {
		s.respond(w, http.StatusConflict, shadowedBody(shadowed))
		return
	}

	p := setup.New(target)

	// **The branch is chosen by the server, not by the user.** There is no radio
	// button asking "is this a new Jellyfin?" — that is a question the software
	// can answer and a person can get wrong, and getting it wrong here is the
	// expensive direction.
	status, err := p.Status(ctx)
	if err != nil {
		code, body := s.adoptFailure(stepError{step: "reaching it", err: err}, target, setup)
		s.respond(w, code, body)
		return
	}
	if !status.WizardCompleted {
		s.respond(w, http.StatusConflict, jellyfinFailureBody{
			Step: "reaching it",
			Error: "there is a jellyfin at " + target +
				" and it has never been set up, so there is no account to sign in with yet",
			Instructions: []string{
				"curator sets up the Jellyfin it starts beside itself — that is the other half of this screen, and it needs no password from you.",
				"For a Jellyfin you run somewhere else, finish its own setup wizard first, then connect to it here.",
			},
		})
		return
	}

	session, err := p.Authenticate(ctx, req.Username, req.Password)
	if err != nil {
		code, body := s.adoptFailure(stepError{step: "signing in", err: err}, target, setup)
		s.respond(w, code, body)
		return
	}

	// Additive, explicit, and the only credential curator ever holds for this
	// server. The password is not stored and is not written anywhere: it was
	// used once, here.
	key, err := p.MintKey(ctx, session, jellyfin.KeyAppName, jellyfin.AdoptConfigured)
	if err != nil {
		code, body := s.adoptFailure(stepError{step: "minting a key", err: err}, target, setup)
		s.respond(w, code, body)
		return
	}
	if key == "" {
		code, body := s.adoptFailure(stepError{
			step: "minting a key",
			err:  fmt.Errorf("jellyfin returned an empty api key: %w", jellyfin.ErrUnexpectedResponse),
		}, target, setup)
		s.respond(w, code, body)
		return
	}

	report, err := s.libraryReport(ctx, p, session, setup.LibraryPath, req.AddLibrary)
	if err != nil {
		code, body := s.adoptFailure(stepError{step: "the library", err: err}, target, setup)
		s.respond(w, code, body)
		return
	}

	// Curator's own call, with curator's own key, before anything says "done".
	check, err := s.checkKey(ctx, setup, target, key)
	if err != nil {
		code, body := s.adoptFailure(stepError{step: "checking the key", err: err}, target, setup)
		s.respond(w, code, body)
		return
	}

	set := map[string]string{
		"jellyfin_url":     target,
		"jellyfin_api_key": key,
		"playback_target":  config.PlaybackJellyfin,
	}
	if req.PublicURL != "" {
		set["jellyfin_public_url"] = req.PublicURL
	}
	if err := s.settings.Writer.WriteSettings(ctx, set, nil); err != nil {
		s.respond(w, http.StatusInternalServerError, jellyfinFailureBody{
			Step:  "record",
			Error: "curator connected to jellyfin but could not save the result: " + err.Error(),
			Instructions: []string{
				"Nothing on your Jellyfin needs undoing — curator only added an API key named " + jellyfin.KeyAppName + ".",
				"In Jellyfin: Dashboard → API Keys, and copy the key named " + jellyfin.KeyAppName + ".",
				"Paste it into this page's Jellyfin section as JELLYFIN_API_KEY, with JELLYFIN_URL set to " + target + ".",
			},
		})
		return
	}

	s.log.Info("jellyfin adopted",
		"url", target, "user", req.Username, "library", report.State, "added", report.Added)

	s.respond(w, http.StatusOK, jellyfinAdoptedBody{
		Username:    req.Username,
		URL:         target,
		PublicURL:   req.PublicURL,
		LibraryPath: setup.LibraryPath,
		Version:     status.Version,
		Library:     report,
		Check:       check,
	})
}

// libraryReport works out which of the three cases the user is in, and adds a
// library when — and only when — they asked for one.
//
// The order is the argument. Coverage is read first because a library that
// already takes curator's path in makes every other question moot; the path
// check runs only when nothing covers it, because it is the thing that decides
// between "offer" and "explain".
func (s *Server) libraryReport(
	ctx context.Context, p Provisioner, session jellyfin.Session, path string, add bool,
) (jellyfinLibraryReport, error) {
	libraries, err := p.Libraries(ctx, session)
	if err != nil {
		return jellyfinLibraryReport{}, err
	}

	report := jellyfinLibraryReport{Names: names(libraries)}
	if covers(libraries, path) {
		report.State = libraryCovered
		report.Detail = "a library on that server already covers " + path + ", so there is nothing to add"
		return report, nil
	}

	// A hint, and the sentence says so. Jellyfin reports the path it sees
	// through its own mount and curator reports the one it sees through its own,
	// and D32 recorded that those two disagree on every deployment where the
	// services reach the disk differently — which is the normal one.
	answer, err := p.PathExists(ctx, session, path)
	if err != nil {
		// Not fatal, on purpose. This is the hint and not the adoption: a server
		// that will not answer a path question has still handed curator a
		// working key, and failing here would refuse an adoption over a
		// diagnostic.
		s.log.Info("jellyfin would not say whether it can see curator's library path",
			"path", path, "err", err)
		answer = jellyfin.PathUnknown
	}

	switch answer {
	case jellyfin.PathAbsent:
		report.State = libraryUnseen
		report.Detail = "that server says " + path + " does not exist on it, so curator will not point a library at it"
		return report, nil
	case jellyfin.PathPresent:
		report.State = libraryAddable
		report.Detail = "no library covers " + path + ", and that server can see it"
	default:
		report.State = libraryUnknown
		report.Detail = "no library covers " + path + ", and that server did not say whether it can see it"
	}

	if !add {
		return report, nil
	}
	// The one write. AdoptConfigured is the caller's explicit opt-in, passed
	// here and nowhere else in this handler.
	if err := p.AddLibrary(ctx, session, libraryName, path, jellyfin.AdoptConfigured); err != nil {
		return jellyfinLibraryReport{}, err
	}
	// Re-read rather than trust the 204, for the reason AddLibrary itself
	// re-reads: a folder created with refreshLibrary=false came back in the
	// listing with no ItemId at all until a refresh materialised it.
	libraries, err = p.Libraries(ctx, session)
	if err != nil {
		return jellyfinLibraryReport{}, err
	}
	report.Names = names(libraries)
	report.Added = true
	report.State = libraryCovered
	report.Detail = "curator added a " + libraryName + " library at " + path + " and changed nothing else"
	return report, nil
}

// covers reports whether any of a server's libraries already takes path in.
func covers(libraries []jellyfin.Library, path string) bool {
	for _, library := range libraries {
		if library.Covers(path) {
			return true
		}
	}
	return false
}

// names lists the libraries a server holds, so the screen can show that curator
// looked at all of them.
func names(libraries []jellyfin.Library) []string {
	out := make([]string, 0, len(libraries))
	for _, library := range libraries {
		if strings.TrimSpace(library.Name) != "" {
			out = append(out, library.Name)
		}
	}
	return out
}

// checkKey proves the minted key with the call that will use it.
//
// The film is one curator already holds, so a hit means the whole Open in
// Jellyfin path works end to end on this pair of servers. A miss does NOT fail
// the adoption: a Jellyfin on another machine that has never seen curator's disk
// is a correct adoption and an empty answer. What fails it is Jellyfin refusing
// the key or the request — that is the case this exists for, and it is invisible
// from every other step of the flow.
func (s *Server) checkKey(
	ctx context.Context, setup *JellyfinSetup, target, key string,
) (jellyfinCheckReport, error) {
	if setup.Reader == nil {
		return jellyfinCheckReport{Detail: "this process cannot make that check"}, nil
	}

	tmdbID, year, title := s.checkFilm(ctx)
	_, err := setup.Reader(target, key).FindMovie(ctx, tmdbID, year)
	switch {
	case err == nil:
		return jellyfinCheckReport{
			Film: title, Found: true,
			Detail: "curator asked that jellyfin for " + title + " with its new key and it answered",
		}, nil
	case errors.Is(err, jellyfin.ErrNotFound) && title == "":
		// The query ran and read the library, which is the half that can fail.
		// There was no particular film to ask for, which is normal on an install
		// whose own library is empty or unmatched.
		return jellyfinCheckReport{
			Detail: "the key works — curator read that server's films with it. " +
				"curator's own library holds nothing TMDB has matched yet, so there was no film to look for",
		}, nil
	case errors.Is(err, jellyfin.ErrNotFound):
		return jellyfinCheckReport{
			Film: title,
			Detail: "the key works — curator read that server's films with it — and " + title +
				" is not among them, which is what a jellyfin that has never seen this disk looks like",
		}, nil
	default:
		return jellyfinCheckReport{}, err
	}
}

// checkFilm picks the film to prove the key with: one curator holds and TMDB has
// matched.
//
// Zero is a supported answer rather than a failure. FindMovie with no id still
// reads /Items and still finds nothing, which is exactly the half of the check
// that can fail — a library with nothing matched in it is a normal state on a
// fresh install and must not skip the check.
func (s *Server) checkFilm(ctx context.Context) (tmdbID, year int, title string) {
	movies, err := s.store.ListMovies(ctx)
	if err != nil {
		s.log.Info("could not read the library to pick a film to check the jellyfin key with", "err", err)
		return 0, 0, ""
	}
	for _, movie := range movies {
		if movie.TMDBID == nil {
			continue
		}
		// MatchYear, not Year: a hand-matched row's folder year is not the one
		// Jellyfin holds, and proving a good key against it would report the key
		// as broken (D37).
		return int(*movie.TMDBID), movie.MatchYear(), movie.Title
	}
	return 0, 0, ""
}

// adoptFailure turns a failure of this branch into the status and the body.
//
// It is not provisionFailure with a flag. The two disagree about the message
// that matters most: a 401 from AuthenticateByName is a typo here and is a
// server behaving strangely there, because provisioning signs in with an account
// curator created two steps earlier and adoption signs in with one the person
// typed.
func (s *Server) adoptFailure(err error, target string, setup *JellyfinSetup) (int, jellyfinFailureBody) {
	body := jellyfinFailureBody{Error: err.Error()}
	var step stepError
	if errors.As(err, &step) {
		body.Step = step.step
		body.Error = step.err.Error()
	}

	switch {
	case errors.Is(err, jellyfin.ErrBadCredentials):
		// 422 and never 401, for the reason provisionFailure gives: curator's
		// own 401 is what web/lib/api.ts turns into the login gate, so answering
		// one here would throw somebody back to curator's password box because
		// JELLYFIN refused something.
		body.Error = "jellyfin refused that username or password"
		body.Instructions = []string{
			"Open " + target + " in a browser and sign in there to check the account.",
			"It has to be an account on that Jellyfin — not a curator password, and not a Jellyfin Connect one.",
			"An administrator account is required: curator has to be allowed to create an API key.",
		}
		return http.StatusUnprocessableEntity, body

	case errors.Is(err, jellyfin.ErrAuthRequestRejected):
		// The message this one must NOT carry is "wrong password". Measured: a
		// missing Authorization: MediaBrowser header is 400 and a wrong password
		// is 401, and telling somebody their password is wrong when curator sent
		// a malformed header is the worst available message — it is the one
		// thing they will retype for ever.
		body.Error = "jellyfin refused the sign-in request itself, which is curator's own doing and not your password"
		body.Instructions = append([]string{
			"Your username and password are not the problem — do not retype them.",
			"This is curator's sign-in request, or a Jellyfin newer than the 10.10.7 it was measured against.",
		}, adoptManualSteps(target, setup.LibraryPath)...)
		return http.StatusBadGateway, body

	case errors.Is(err, jellyfin.ErrUnauthorized):
		body.Instructions = append([]string{
			"That account signed in, and then Jellyfin refused it something. An administrator account is required to create an API key.",
		}, adoptManualSteps(target, setup.LibraryPath)...)
		return http.StatusBadGateway, body

	case errors.Is(err, jellyfin.ErrUnreachable):
		body.Instructions = []string{
			"Nothing answered at " + target + ".",
			"Check the address, and that this machine can reach it — curator runs in a container, so `localhost` here is curator's own container and not yours.",
		}
		return http.StatusServiceUnavailable, body

	case errors.Is(err, jellyfin.ErrNotReady):
		body.Instructions = []string{
			"That Jellyfin is up and still loading. Wait a few seconds and try again — a cold start took 14 to 27 seconds when this was measured.",
		}
		return http.StatusServiceUnavailable, body
	}

	body.Instructions = adoptManualSteps(target, setup.LibraryPath)
	return http.StatusBadGateway, body
}

// adoptManualSteps is this branch's fallback, and it is a different list from
// provisioning's: there is no wizard to run here, because the server already has
// one somebody completed.
func adoptManualSteps(target, libraryPath string) []string {
	return []string{
		"In Jellyfin: Dashboard → API Keys → new key.",
		"Paste it into this page's Jellyfin section as JELLYFIN_API_KEY, with JELLYFIN_URL set to " + target + ".",
		"If no library on that server covers " + libraryPath + ", and it can see that path, add a " +
			libraryName + " library there yourself.",
	}
}
