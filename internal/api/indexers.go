package api

// The Indexers screen's one endpoint: is minter there?
// (docs/tasks/T49-minter-on-demand.md)
//
// It is the same shape as the Playback screen's Jellyfin probe and deliberately
// so — a probe, a pasted command, and a service that reports itself unconfigured
// until something answers. minter and Jellyfin are the only two things in this
// product that need a second container, curator cannot start either of them
// (docs/decisions.md D34: no Docker socket, for any reason), and one shape for
// both is one fewer concept than a bespoke flow per companion service.
//
// Not in settings.go, for the reason its Jellyfin counterpart is not: this is a
// call out to another service with a failure mode of its own, and the settings
// screen's own integration probes are a batch that answers a different question
// — "what is this process running", once, on a button — while this one is polled
// beside a toggle somebody is in the middle of switching on.

import (
	"context"
	"errors"
	"net/http"

	"github.com/DulsaraNethmin/curator/internal/indexer"
)

// MinterProber is the reachability check this package needs, which is minter's
// /health and nothing else.
//
// Declared here rather than taking *indexer.Minter for the same reason
// Provisioner is: what the handler can reach is then exactly what it needs. In
// particular it cannot Fetch — a probe that could render a page would eventually
// be made to, and that is a real Firefox and ~9 s to answer "are you up".
type MinterProber interface {
	URL() string
	Probe(ctx context.Context) (indexer.Health, error)
}

// IndexerSetup is what the Indexers screen needs behind it.
type IndexerSetup struct {
	// Minter is the probe. Nil leaves the endpoint answering 503, which is the
	// honest state for a process with no way to reach one.
	Minter MinterProber

	// X1337 is whether the RUNNING process has the 1337x indexer switched on —
	// cfg.IndexerX1337, not the stored setting.
	//
	// The distinction is the field's whole reason for existing. A setting
	// written through the API applies at the next start (docs/decisions.md
	// D29), so the ordinary way through this screen is: switch 1337x on, save,
	// paste the command, and watch minter come up while curator is still
	// running with 1337x off. Reporting the stored value would say the indexer
	// is on when no search will use it, which is the silent half of exactly the
	// failure this task exists to remove.
	X1337 bool
}

// WithIndexers attaches the Indexers screen's dependencies.
func (s *Server) WithIndexers(setup IndexerSetup) *Server {
	s.indexers = &setup
	return s
}

// RegisterIndexers mounts the Indexers screen's route.
func (s *Server) RegisterIndexers(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/indexers/minter/probe", s.handleMinterProbe)
}

// The three worlds the probe distinguishes.
//
// There is no "starting" here, unlike Jellyfin's four. Jellyfin accepts the
// connection and answers 503 while it loads, which is a state worth its own
// sentence; minter's port is not open until uvicorn is, so there is nothing to
// distinguish — it is measured at 45 s from `up -d` to healthy on this laptop,
// all of it with nothing listening.
const (
	minterUnreachable = "unreachable"
	minterUnhealthy   = "unhealthy"
	minterReady       = "ready"
)

// minterCommand is the one line phase 9 accepts as the cost of refusing the
// Docker socket, for minter's half. The profile is compose.yaml's, and `1337x`
// is a legal profile name — checked, because profile names must start
// alphanumeric and a leading digit looked like a plausible way to lose an
// afternoon.
//
// A constant here rather than a string in the UI for the same reason
// bundleCommand is one: two spellings of a profile name is a service that never
// starts and never errors.
const minterCommand = "docker compose --profile 1337x up -d"

// minterProbeBody is what GET /api/indexers/minter/probe reports.
type minterProbeBody struct {
	State string `json:"state"`
	URL   string `json:"url"`

	// Enabled is whether THIS PROCESS is searching 1337x. See IndexerSetup.X1337
	// — it is the running value, and a screen that can see both it and the form
	// above it is the only place the restart is visible before somebody searches
	// and finds nothing.
	Enabled bool `json:"enabled"`

	// Command is the line the user pastes.
	Command string `json:"command"`

	// UserAgent is only ever present when minter answered its own health check.
	// It is reported because it is the one field that proves this is minter's
	// patched Firefox rather than something else on the port, and D2's whole
	// argument is that no other fingerprint substitutes for it.
	UserAgent string `json:"user_agent,omitempty"`

	Detail string `json:"detail,omitempty"`
}

// handleMinterProbe reports which of the three worlds minter is in.
//
// Always 200, and the state IS the answer — the same rule the Jellyfin probe
// follows. A probe that 503s when nothing is listening makes "the container is
// not up yet", which is the expected state for most of this screen's life, look
// identical to "curator is broken" in a browser's network tab.
//
// Nothing is cached, deliberately. A minter that was up when the page loaded and
// is down now must not be reported as up because an answer was kept, so every
// call is a fresh request — which is affordable precisely because it is /health
// and not a page fetch.
func (s *Server) handleMinterProbe(w http.ResponseWriter, r *http.Request) {
	setup := s.indexers
	if setup == nil || setup.Minter == nil {
		s.fail(w, http.StatusServiceUnavailable,
			errors.New("this process has no minter to probe"))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
	defer cancel()

	body := minterProbeBody{
		URL:     setup.Minter.URL(),
		Enabled: setup.X1337,
		Command: minterCommand,
	}

	health, err := setup.Minter.Probe(ctx)
	switch {
	case err == nil && health.OK:
		body.State = minterReady
		body.UserAgent = health.UserAgent
		body.Detail = "minter answered and is ready to fetch pages"
	case err == nil:
		// Reachable, and saying it is not well. minter's own health check is
		// what the container's HEALTHCHECK reads, so this is its verdict on
		// itself rather than curator's guess about it.
		body.State = minterUnhealthy
		body.UserAgent = health.UserAgent
		body.Detail = "minter answered but reports it is not ready — its browser may still be starting"
	case errors.Is(err, indexer.ErrUnreachable):
		body.State = minterUnreachable
		body.Detail = "nothing answered"
	default:
		// Something answered and it was not minter: a redirect, a proxy, another
		// service on the port. Reported as unhealthy rather than unreachable
		// because the instructions differ — running the compose command again
		// will not help an address that already has something on it, and the
		// error names what it was.
		body.State = minterUnhealthy
		body.Detail = err.Error()
	}

	s.respond(w, http.StatusOK, body)
}
