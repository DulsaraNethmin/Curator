package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/DulsaraNethmin/curator/internal/update"
)

// UpdateSetup is what the Updates screen needs.
//
// Declared as an interface-free struct of concrete types because there is
// exactly one implementation of each and a seam here would be scaffolding for a
// second caller that does not exist.
type UpdateSetup struct {
	// Checker reads the release feed. Nil means UPDATE_CHECK is off, and the
	// screen says only which version is running — a valid, chosen state rather
	// than a broken one.
	Checker *update.Checker

	// Updater installs it. Nil is the ordinary state for an install with no
	// watchtower beside it, and it is what turns the button into a command.
	Updater *update.Updater

	// Command is the line to paste when there is no updater. It is passed in
	// rather than built here because only cmd/curator knows whether this
	// process was started by compose or by `docker run`.
	Command string

	// Busy reports whether anything is downloading. An update restarts the
	// container, and the engine resumes from boltdb, but a person who is
	// watching a progress bar deserves to be told before it disappears.
	Busy func(ctx context.Context) bool
}

// WithUpdates attaches the Updates screen's dependencies.
func (s *Server) WithUpdates(setup UpdateSetup) *Server {
	s.updates = &setup
	return s
}

// RegisterUpdates mounts the Updates routes.
func (s *Server) RegisterUpdates(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/update", s.handleUpdateStatus)
	mux.HandleFunc("POST /api/update", s.handleUpdateTrigger)
}

// updateBody is what the screen renders. It embeds update.Status and adds the
// two things only this layer knows: whether pressing the button would do
// anything, and what to type instead.
type updateBody struct {
	update.Status

	// Command is shown when CanInstall is false. It is never hidden behind the
	// button: somebody who wants to update by hand should not have to configure
	// an updater to find out how.
	Command string `json:"command,omitempty"`

	// Busy is true when a download is running. Advisory — it does not refuse
	// the update, because the person pressing the button may know exactly what
	// they are doing and the engine resumes either way.
	Busy bool `json:"busy"`

	// Checking is false when UPDATE_CHECK is off, which is why Latest is empty.
	// Without this the screen cannot tell "no newer version" from "never
	// looked", and those are different sentences.
	Checking bool `json:"checking"`
}

func (s *Server) handleUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if s.updates == nil {
		s.fail(w, http.StatusServiceUnavailable, errors.New("this process has no update checker"))
		return
	}
	setup := s.updates

	body := updateBody{Command: setup.Command}
	body.Current = s.version()

	if setup.Checker != nil {
		body.Checking = true
		// `force` is the caller's, so the "Check again" button re-asks and a
		// page load does not. The cache is what keeps a household behind one
		// address inside GitHub's hourly limit.
		body.Status = setup.Checker.Check(r.Context(), r.URL.Query().Get("force") == "true")
	}

	// Configured, not reachable — and the difference is load-bearing. See the
	// note on update.Updater.Configured: watchtower's update endpoint is a GET,
	// so a reachability probe would restart curator every time this screen
	// loaded.
	body.CanInstall = setup.Updater.Configured()

	if setup.Busy != nil {
		body.Busy = setup.Busy(r.Context())
	}

	s.respond(w, http.StatusOK, body)
}

func (s *Server) handleUpdateTrigger(w http.ResponseWriter, r *http.Request) {
	if s.updates == nil {
		s.fail(w, http.StatusServiceUnavailable, errors.New("this process has no updater"))
		return
	}

	err := s.updates.Updater.Trigger(r.Context())
	switch {
	case err == nil:
		// 202, not 200. The work has been accepted by something else and this
		// process is about to be replaced by it, so there is nothing here that
		// can report the outcome — see the note at the top of internal/update.
		s.respond(w, http.StatusAccepted, map[string]string{
			"state": "restarting",
			"detail": "the updater has been asked to pull and restart curator. " +
				"This page will stop answering for a moment; reload it to see the new version.",
		})
	case errors.Is(err, update.ErrNoUpdater):
		// 409 rather than 500: nothing failed, the install simply has no
		// updater, and the screen already knows what to say instead.
		s.fail(w, http.StatusConflict, errors.New(
			"no updater is configured, so curator cannot install this itself: "+s.updates.Command))
	default:
		s.fail(w, http.StatusBadGateway, err)
	}
}

// version is the running build, which the settings view already carries.
func (s *Server) version() string {
	if s.settings != nil {
		return s.settings.Version
	}
	return ""
}
