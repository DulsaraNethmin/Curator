package api

// The Playback screen's two endpoints: find out what is at the other end, and
// set it up (docs/tasks/T65-playback-screen.md).
//
// Provisioning is not a setting write, which is why it is here and not in
// settings.go: it is eight calls to another server with failure modes of its
// own, and it *ends* by writing settings through the machinery phase 7 already
// built rather than beside it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
}

// WithJellyfinSetup attaches the Playback screen's dependencies.
//
// Nil New leaves the endpoints answering 503, which is the honest state for a
// process that has no way to reach a Jellyfin.
func (s *Server) WithJellyfinSetup(setup JellyfinSetup) *Server {
	s.jellyfinSetup = &setup
	return s
}

// RegisterJellyfin mounts the Playback screen's two routes.
func (s *Server) RegisterJellyfin(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/jellyfin/probe", s.handleJellyfinProbe)
	mux.HandleFunc("POST /api/jellyfin/provision", s.handleJellyfinProvision)
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
	setup := s.jellyfinSetup
	if setup == nil || setup.New == nil {
		s.fail(w, http.StatusServiceUnavailable,
			errors.New("this process cannot set up a jellyfin"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
	defer cancel()

	body := jellyfinProbeBody{
		URL:         setup.URL,
		LibraryPath: setup.LibraryPath,
		Command:     bundleCommand,
	}

	status, err := setup.New(setup.URL).Status(ctx)
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
		body.Detail = err.Error()
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
	setup := s.jellyfinSetup
	if setup == nil || setup.New == nil {
		s.fail(w, http.StatusServiceUnavailable,
			errors.New("this process cannot set up a jellyfin"))
		return
	}
	if s.settings == nil || s.settings.Writer == nil {
		// Refused before anything is touched, and this ordering is the point:
		// a curator that set a Jellyfin up and then could not record the key
		// would leave the user with a configured media server curator cannot
		// talk to, and no way back short of deleting a volume.
		s.fail(w, http.StatusServiceUnavailable,
			errors.New("this process cannot write settings, so it will not set up a jellyfin it cannot record"))
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
	if shadowed, err := s.shadowedByEnv(ctx, req); err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	} else if len(shadowed) > 0 {
		s.respond(w, http.StatusConflict, jellyfinFailureBody{
			Step: "record",
			Error: fmt.Sprintf(
				"%s set in the environment, which beats anything saved — curator would set jellyfin up and then be unable to record it",
				strings.Join(shadowed, " and ")),
			Instructions: []string{
				"Unset " + strings.Join(shadowed, " and ") + " where curator is started, then try again.",
				"In the bundle that means removing it from compose.yaml's environment: curator ships without it on purpose.",
			},
		})
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
// provision is about to write.
//
// Only the keys it will actually write: an env-owned jellyfin_public_url does
// not matter when the browser sent nothing to put in it.
func (s *Server) shadowedByEnv(ctx context.Context, req jellyfinProvisionRequest) ([]string, error) {
	states, err := s.settings.states(ctx)
	if err != nil {
		return nil, err
	}

	keys := []string{"jellyfin_url", "jellyfin_api_key", "playback_target"}
	if req.PublicURL != "" {
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
