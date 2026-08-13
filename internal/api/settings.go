package api

import (
	"context"
	"net/http"
	"sync"
	"time"
)

// probeTimeout bounds one reachability check. It is short on purpose: the
// settings screen is answering "is this thing there", and a caller waiting
// thirty seconds for that has already learned the answer.
const probeTimeout = 5 * time.Second

// Integration is one thing curator talks to, as the settings screen sees it.
//
// There is no field for a credential, and that is the design rather than an
// omission. There is no authentication in front of this endpoint and there is
// not going to be (docs/decisions.md D17), so the only safe shape is one where
// the secret is never in the payload to leak — not truncated, not hashed, not
// masked, since a masked secret still confirms its length and its existence to
// anyone on the LAN. `Configured` is the only fact a screen actually needs: its
// real question is "can I press this button", never "what is the password".
type Integration struct {
	Name       string
	Env        string
	Configured bool
	Detail     string

	// Probe performs a READ-ONLY check that the service is reachable. A nil
	// Probe means there is no safe way to ask — Jellyfin is the live example:
	// the only call internal/jellyfin makes is /Library/Refresh, which mutates,
	// and a settings page must not queue a library scan just to render itself.
	Probe func(ctx context.Context) error
}

// Settings is everything GET /api/settings reports. It is built by cmd/curator
// out of the config, rather than this package importing config, so that
// internal/api keeps knowing nothing about where settings come from.
type Settings struct {
	Version      string
	Integrations []Integration
	Paths        map[string]string
	Intervals    map[string]string
}

// WithSettings attaches the settings view and returns the server.
func (s *Server) WithSettings(set Settings) *Server {
	s.settings = &set
	return s
}

// RegisterSettings mounts phase 5's one new route.
func (s *Server) RegisterSettings(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/settings", s.handleSettings)
}

// integrationBody is one integration as JSON.
//
// Reachable is a pointer so that "not set up" and "set up but broken" stay
// distinguishable: an unconfigured integration omits the field entirely rather
// than reporting false, because the screen says quite different things about
// the two and a bare false would collapse them.
type integrationBody struct {
	Name       string `json:"name"`
	Env        string `json:"env"`
	Configured bool   `json:"configured"`
	Reachable  *bool  `json:"reachable,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

type settingsBody struct {
	Version      string            `json:"version"`
	Probed       bool              `json:"probed"`
	Integrations []integrationBody `json:"integrations"`
	Paths        map[string]string `json:"paths"`
	Intervals    map[string]string `json:"intervals"`
}

// handleSettings reports what is configured and, on request, what is reachable.
//
// Probing is opt-in via ?probe=1 because it calls out to real services, one of
// which (minter, behind 1337x) may wake a browser. A settings page that takes
// thirteen seconds to render is a settings page nobody opens, so the default is
// to answer from configuration alone and instantly.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	set := s.settings
	if set == nil {
		set = &Settings{}
	}

	probe := r.URL.Query().Get("probe") == "1"
	bodies := make([]integrationBody, len(set.Integrations))

	var wg sync.WaitGroup
	for i, integration := range set.Integrations {
		bodies[i] = integrationBody{
			Name:       integration.Name,
			Env:        integration.Env,
			Configured: integration.Configured,
			Detail:     integration.Detail,
		}

		// An unconfigured integration is never probed. There is nothing to probe
		// with, and a failure would report "unreachable" for something that was
		// simply never set up.
		if !probe || !integration.Configured || integration.Probe == nil {
			continue
		}

		wg.Add(1)
		go func(i int, run func(context.Context) error) {
			defer wg.Done()

			ctx, cancel := context.WithTimeout(r.Context(), probeTimeout)
			defer cancel()

			reachable := true
			if err := run(ctx); err != nil {
				// A failed probe is the news this page exists to carry, so it is
				// never an error status. 200 with bad news is the whole point.
				reachable = false
				bodies[i].Detail = err.Error()
			}
			bodies[i].Reachable = &reachable
		}(i, integration.Probe)
	}
	wg.Wait()

	paths, intervals := set.Paths, set.Intervals
	if paths == nil {
		paths = map[string]string{}
	}
	if intervals == nil {
		intervals = map[string]string{}
	}

	s.respond(w, http.StatusOK, settingsBody{
		Version:      set.Version,
		Probed:       probe,
		Integrations: bodies,
		Paths:        paths,
		Intervals:    intervals,
	})
}
