package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DulsaraNethmin/curator/internal/logs"
	"github.com/DulsaraNethmin/curator/internal/settings"
)

// probeTimeout bounds one reachability check. It is short on purpose: the
// settings screen is answering "is this thing there", and a caller waiting
// thirty seconds for that has already learned the answer.
const probeTimeout = 5 * time.Second

// Integration is one thing curator talks to, as the settings screen sees it.
//
// There is no field for a credential, and that is the design rather than an
// omission. There is no authentication in front of this endpoint by default
// (docs/decisions.md D17, and D25 keeps it off unless somebody turns it on), so
// the only safe shape is one where the secret is never in the payload to leak —
// not truncated, not hashed, not masked, since a masked secret still confirms
// its length and its existence to anyone on the LAN. `Configured` is the only
// fact a screen actually needs: its real question is "can I press this button",
// never "what is the password".
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

// SettingState is one setting as cmd/curator sees it: the value this process is
// actually running on, where that came from, and what is saved and waiting for
// the next start.
//
// **No secret value is ever in one.** cmd/curator strips them before they cross
// into this package and the handler omits them again on the way out. Two
// independent guards, because the single thing this endpoint must never do is
// send a secret to a browser — and the second guard is the one that still holds
// when somebody adds a field in phase 8.
type SettingState struct {
	// Value is what this process resolved for the setting. Empty for a secret,
	// and empty is also a legitimate value for DOWNLOADS_PATH, which is why
	// Configured is a separate fact rather than `Value != ""`.
	Value string

	// Source is env, stored or default. It is what stops the shadow case being
	// a silent failure: a value the environment owns is reported and rendered
	// read-only rather than typed, saved and quietly ignored.
	Source settings.Source

	// Configured reports that something set this — the environment or the store,
	// not a default. For a secret it is the whole of what a screen may know.
	Configured bool

	// Pending is what the next start will use, when that differs from Value.
	// Empty for a secret, exactly as Value is: that a secret has changed is
	// reportable, what it changed to is not.
	Pending string

	// PendingChange says a saved value differs from the running one. It is a
	// separate flag rather than `Pending != ""` because a cleared setting has a
	// pending change to a default, and because a secret has one with nothing to
	// show for it (docs/decisions.md D29).
	PendingChange bool

	// Unreadable reports a stored value that could not be decrypted — a database
	// restored without its key file. The setting behaves as unset and asks to be
	// entered again, which is recoverable; being told it is simply unset is what
	// would make it permanent (docs/decisions.md D28).
	Unreadable bool
}

// Writer persists a settings change.
//
// One method, and one transaction behind it: a form with eight changed fields
// either applies all eight or none, because half a configuration is not a state
// anything downstream reasons about. set holds plaintext and clear holds keys to
// delete — the encryption of a secret and the hashing of a password happen on
// the other side of this interface, in cmd/curator, so this package keeps
// knowing nothing about either.
type Writer interface {
	WriteSettings(ctx context.Context, set map[string]string, clear []string) error
}

// Access is the live authentication state — the one thing a settings write
// applies at once rather than at the next start.
//
// A password that took effect after a restart would leave you unprotected until
// then, which inverts the point of setting one, so D29 makes it the single
// exception. It is one method so that this package can apply the exception
// without knowing what authentication is: T41 owns the holder, the hashing and
// the middleware, and this handler owns only the fact that a write to auth_*
// must not wait for a restart.
//
// Nil is a supported state, not a broken one: with no authentication in the
// process there is nothing for a write to apply.
type Access interface {
	// Reload re-reads the stored settings that apply at once and installs them
	// for the next request.
	Reload(ctx context.Context) error
}

// AccessFunc adapts a function to Access, so cmd/curator can compose more than
// one immediate subsystem behind the single hook this handler calls.
//
// There are two now — the password and the VPN kill switch — and one write may
// touch both. Same idiom as ScannerFunc.
type AccessFunc func(ctx context.Context) error

func (f AccessFunc) Reload(ctx context.Context) error { return f(ctx) }

// Settings is everything GET /api/settings reports. It is built by cmd/curator
// out of the config, rather than this package importing config, so that
// internal/api keeps knowing nothing about where settings come from.
type Settings struct {
	Version      string
	Integrations []Integration
	Paths        map[string]string
	Intervals    map[string]string

	// States reads what every setting currently is. A function rather than a
	// map, because a PUT changes the answer: a GET one request later has to
	// report what is now pending, and a snapshot taken at start-up never could.
	//
	// Nil is phase 5's read-only view — no form, and an empty settings array.
	States func(ctx context.Context) (map[string]SettingState, error)

	// Writer applies a change. Nil means this process cannot write settings, and
	// PUT says so rather than accepting a body it will drop.
	Writer Writer

	// Access is T41's live password holder. See the interface.
	Access Access
}

// WithSettings attaches the settings view and returns the server.
func (s *Server) WithSettings(set Settings) *Server {
	s.settings = &set
	return s
}

// RegisterSettings mounts the settings routes: phase 5's read, and phase 7's
// write.
func (s *Server) RegisterSettings(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/settings", s.handleSettings)
	mux.HandleFunc("PUT /api/settings", s.handleSettingsWrite)
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

// settingBody is one registry row as the form sees it.
//
// Value is a pointer because absent and empty are different answers: it is
// absent for every secret, always, and present as "" for DOWNLOADS_PATH, where
// empty is a deliberate setting meaning "use the path qBittorrent reported".
// Collapsing the two into `omitempty` on a plain string would make a cleared
// path indistinguishable from a withheld key.
type settingBody struct {
	Key     string   `json:"key"`
	Group   string   `json:"group"`
	Kind    string   `json:"kind"`
	Options []string `json:"options,omitempty"`
	Secret  bool     `json:"secret"`

	// Env is what the screen names when it renders a shadowed field read-only.
	// Without it the UI would have to carry its own copy of the registry, which
	// is the second vocabulary this whole phase exists to avoid.
	Env string `json:"env"`

	// FileEnv is the second variable, for the one setting that has one. It is
	// here for the same reason Env is, and it was missing: VPN_CONFIG_FILE is
	// how a provider's .conf is actually supplied — it is what the developer's
	// own .env sets — so a screen with only Env renders "set by VPN_CONFIG" at
	// exactly the moment VPN_CONFIG_FILE is the variable to unset. envOwner()
	// already names both in the 409 that arrives late; this is the same
	// sentence arriving on time.
	FileEnv string `json:"file_env,omitempty"`

	Value      *string `json:"value,omitempty"`
	Configured bool    `json:"configured"`
	Source     string  `json:"source"`
	Editable   bool    `json:"editable"`
	Unreadable bool    `json:"unreadable,omitempty"`

	Pending         *string `json:"pending"`
	PendingChange   bool    `json:"pending_change"`
	RestartRequired bool    `json:"restart_required"`
}

// settingsBody is the whole response.
//
// The first five fields are phase 5's and keep their exact shapes — phase 5
// verified them and T42's own screen reads `integrations`. `settings` is
// additive and last.
type settingsBody struct {
	Version      string            `json:"version"`
	Probed       bool              `json:"probed"`
	Integrations []integrationBody `json:"integrations"`
	Paths        map[string]string `json:"paths"`
	Intervals    map[string]string `json:"intervals"`
	Settings     []settingBody     `json:"settings"`
}

// handleSettings reports what is configured and, on request, what is reachable.
//
// Probing is opt-in via ?probe=1 because it calls out to real services, one of
// which (minter, behind 1337x) may wake a browser. A settings page that takes
// thirteen seconds to render is a settings page nobody opens, so the default is
// to answer from configuration alone and instantly.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	body, err := s.settingsBody(r.Context(), r.URL.Query().Get("probe") == "1")
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	s.respond(w, http.StatusOK, body)
}

// handleSettingsWrite applies a partial settings object in one transaction.
//
// It is silent about values at every level. No debug line of the body, no error
// that wraps one, not even a rejected value quoted back in the message that
// rejects it — the log buffer's scrubber only knows the secrets it was handed at
// start-up, and the key somebody is typing right now is by definition not one of
// them (docs/phase-7.md, the traps).
func (s *Server) handleSettingsWrite(w http.ResponseWriter, r *http.Request) {
	set := s.settings
	if set == nil || set.Writer == nil {
		s.fail(w, http.StatusServiceUnavailable, errors.New("this process cannot write settings"))
		return
	}
	ctx := r.Context()

	// Decoded into a map, never a struct. A settings write is a partial update —
	// absent leaves alone, "" clears — and the zero value of every field of a
	// struct arrives looking like an instruction to erase it.
	var body map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.fail(w, http.StatusBadRequest,
			errors.New("body is not a JSON object of setting keys to string values"))
		return
	}

	states, err := set.states(ctx)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	var (
		// Rejected is 400 and shadowed is 409: a value the environment owns was
		// fine, it was the destination that was not.
		rejected  = map[string]string{}
		shadowed  = map[string]string{}
		toSet     = map[string]string{}
		toClear   []string
		immediate bool
	)
	for key, raw := range body {
		item, ok := settings.Get(key)
		if !ok {
			// Named, never ignored. A typo that silently does nothing is the
			// worst outcome a settings screen has: it looks exactly like a save.
			rejected[key] = "not a setting"
			continue
		}

		var value string
		switch {
		case string(raw) == "null":
			// Rejected rather than guessed at: null is neither "leave it alone",
			// which is omitting the key, nor "clear it", which is "".
			rejected[key] = `want a string: null is neither "leave it alone" (omit the key) nor "clear it" ("")`
			continue
		case json.Unmarshal(raw, &value) != nil:
			rejected[key] = "want a string — a setting carries the same text its environment variable would"
			continue
		}
		value = strings.TrimSpace(value)

		if states[key].Source == settings.SourceEnv {
			shadowed[key] = envOwner(item) + " is set in the environment, which wins here: unset it to configure this from the screen"
			continue
		}

		if item.Immediate {
			immediate = true
		}
		if value == "" {
			toClear = append(toClear, key)
			continue
		}
		if err := settings.Validate(key, value); err != nil {
			// The message the next start-up would have printed, because it comes
			// from the parser that would print it — with anything recognisably
			// from the value taken back out when the value is a secret.
			message := err.Error()
			if item.Secret {
				message = redactValue(message, value)
			}
			rejected[key] = message
			continue
		}
		toSet[key] = value
	}

	// The one ordering mistake that locks somebody out of their own library.
	if enabled, password := authAfter(states, toSet, toClear); enabled && !password {
		rejected[settings.Key("AUTH_ENABLED")] = "there is no password to log in with: " +
			"set auth_password in the same request, or before this one"
	}

	if len(rejected) > 0 {
		s.failFields(w, http.StatusBadRequest, "nothing was written: some settings were rejected", rejected)
		return
	}
	if len(shadowed) > 0 {
		s.failFields(w, http.StatusConflict,
			"nothing was written: some settings are set in the environment, which wins", shadowed)
		return
	}

	if len(toSet) > 0 || len(toClear) > 0 {
		if err := set.Writer.WriteSettings(ctx, toSet, toClear); err != nil {
			s.fail(w, http.StatusInternalServerError, err)
			return
		}

		// The keys and the count. Never a value, at any level: this line goes
		// into the buffer GET /api/logs serves.
		changed := changedKeys(toSet, toClear)
		s.log.Info("settings written", "keys", strings.Join(changed, " "), "count", len(changed))

		if immediate && set.Access != nil {
			if err := set.Access.Reload(ctx); err != nil {
				// Saved and not applied is a state to report loudly rather than
				// answer 200 to: somebody who has just turned authentication on
				// would otherwise believe it is on.
				s.fail(w, http.StatusInternalServerError,
					fmt.Errorf("saved, but it did not take effect and will not until curator restarts: %w", err))
				return
			}
		}
	}

	// The same body GET would return, and never a probe: ?probe=1 is opt-in and
	// stays opt-in, because it calls out to real services and one of them can
	// wake a browser.
	out, err := s.settingsBody(ctx, false)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	s.respond(w, http.StatusOK, out)
}

// settingsBody builds the response both handlers answer with.
func (s *Server) settingsBody(ctx context.Context, probe bool) (settingsBody, error) {
	set := s.settings
	if set == nil {
		set = &Settings{}
	}

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

			ctx, cancel := context.WithTimeout(ctx, probeTimeout)
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

	rows, err := s.settingRows(ctx, set)
	if err != nil {
		return settingsBody{}, err
	}

	return settingsBody{
		Version:      set.Version,
		Probed:       probe,
		Integrations: bodies,
		Paths:        paths,
		Intervals:    intervals,
		Settings:     rows,
	}, nil
}

// settingRows joins the registry with what this process resolved.
//
// The registry is the catalogue — key, group, kind, options, whether it is a
// secret — and it is imported rather than passed in, because a copy of it in
// cmd/curator would be the second answer that drifts. What is passed in is the
// part this package must not know: where a value came from and what it is.
func (s *Server) settingRows(ctx context.Context, set *Settings) ([]settingBody, error) {
	if set.States == nil {
		// [] and never null: the screen iterates it, and phase 5's read-only
		// view — which every existing test builds — has no form to render.
		return []settingBody{}, nil
	}

	states, err := set.States(ctx)
	if err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}

	catalogue := settings.All()
	rows := make([]settingBody, 0, len(catalogue))
	for _, item := range catalogue {
		state := states[item.Key]
		row := settingBody{
			Key:        item.Key,
			Group:      item.Group,
			Kind:       string(item.Kind),
			Options:    item.Options,
			Secret:     item.Secret,
			Env:        item.Env,
			FileEnv:    item.FileEnv,
			Configured: state.Configured,
			Source:     string(state.Source),
			// A value the environment owns is not editable here, and saying so
			// is what stops it being typed, saved and silently ignored.
			Editable:        state.Source != settings.SourceEnv,
			Unreadable:      state.Unreadable,
			PendingChange:   state.PendingChange,
			RestartRequired: !item.Immediate,
		}

		// The second of the two guards. cmd/curator does not put a secret in a
		// SettingState; this makes the handler incapable of emitting one even if
		// it did, which is the guard that still holds for a field added later.
		if !item.Secret {
			value := state.Value
			row.Value = &value
			if state.PendingChange {
				pending := state.Pending
				row.Pending = &pending
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

// states reads the current state of every setting, tolerating a view that has
// none — a Server built without settings wiring answers the phase 5 shape.
func (s *Settings) states(ctx context.Context) (map[string]SettingState, error) {
	if s.States == nil {
		return map[string]SettingState{}, nil
	}
	return s.States(ctx)
}

// failFields answers with phase 1's {"error": ...} envelope plus a per-field
// map. The envelope is unchanged, so every existing client keeps working; the
// map is what a form needs in order to put a message under the right input.
//
// It never logs. The messages can quote a rejected value, which is the one
// thing a settings failure must not put in a log line.
func (s *Server) failFields(w http.ResponseWriter, status int, message string, fields map[string]string) {
	s.respond(w, status, map[string]any{"error": message, "fields": fields})
}

// authAfter reports what authentication would look like once this write lands.
//
// It is computed over the resulting state rather than over the request, because
// the mistake being caught has three spellings: enabling authentication with no
// password, enabling it while clearing the password, and clearing the password
// while it is already on. All three end with a switch that is on and nothing
// that can satisfy it, which is a lockout from a screen rather than a warning.
func authAfter(states map[string]SettingState, set map[string]string, clear []string) (enabled, password bool) {
	enabledKey, passwordKey := settings.Key("AUTH_ENABLED"), settings.Key("AUTH_PASSWORD")

	enabled = truthy(states[enabledKey].Value)
	password = states[passwordKey].Configured

	if v, ok := set[enabledKey]; ok {
		enabled = truthy(v)
	}
	if _, ok := set[passwordKey]; ok {
		password = true
	}
	for _, key := range clear {
		switch key {
		case enabledKey:
			// Cleared is the default, and the default is off (D25).
			enabled = false
		case passwordKey:
			password = false
		}
	}
	return enabled, password
}

// truthy reads a stored boolean the way config.ParseBool would, and treats
// anything unparseable as off — a settings screen asking "is authentication on"
// wants the safe reading of a value that should not exist.
func truthy(value string) bool {
	v, _ := strconv.ParseBool(value)
	return v
}

// envOwner names the variable that is shadowing a stored value.
//
// VPN_CONFIG is the one setting with two of them, because a provider hands out
// a file and a container hands out a variable, and the message has to name both
// rather than send somebody to unset the one they did not set.
func envOwner(item settings.Setting) string {
	if item.FileEnv != "" {
		return item.Env + " (or " + item.FileEnv + ")"
	}
	return item.Env
}

// changedKeys is what a write logs: sorted, so the line is stable, and keys
// only.
func changedKeys(set map[string]string, clear []string) []string {
	keys := make([]string, 0, len(set)+len(clear))
	for key := range set {
		keys = append(keys, key)
	}
	keys = append(keys, clear...)
	sort.Strings(keys)
	return keys
}

// redactValue takes anything recognisably from value back out of a validation
// message.
//
// vpn.ParseConfig quotes the line it could not parse, and for the one multiline
// secret in the registry that line can be `PrivateKey = ...`. A rejected write
// is not a licence to send a key back to the browser (docs/decisions.md D17,
// D28), and the message is worth keeping otherwise — "[Interface] PrivateKey is
// missing" is exactly what the person pasting a .conf needs to read.
//
// Whole lines first, because that is what the "not `key = value`" message
// quotes, then the tokens inside them.
//
// The length floor is what keeps the message worth reading. At eight characters
// this ate its own explanation — a config missing its key answered "vpn config:
// [redacted] [redacted] is missing", because `[Interface]` and `PrivateKey`
// are in the submitted text as well as in the message. Twenty is longer than
// any word a parser puts in a sentence and shorter than anything these settings
// carry as a credential: a WireGuard key is 44 base64 characters and a TMDB key
// is 32. Measured against the real messages, not chosen.
const redactFrom = 20

func redactValue(message, value string) string {
	for _, line := range strings.Split(value, "\n") {
		if line = strings.TrimSpace(line); len(line) >= redactFrom {
			message = strings.ReplaceAll(message, line, logs.Redacted)
		}
	}
	for _, token := range strings.FieldsFunc(value, func(r rune) bool {
		return r == '\n' || r == '\r' || r == '\t' || r == ' ' || r == '=' || r == '"' || r == ','
	}) {
		if len(token) >= redactFrom {
			message = strings.ReplaceAll(message, token, logs.Redacted)
		}
	}
	return message
}
