package jellyfin

// Provisioning: the only writes in this package, and the guard that keeps them
// off a server somebody is already watching (docs/decisions.md D34).
//
// Every status handled below was observed against a throwaway
// jellyfin/jellyfin:10.10.7 on 2026-08-15, request by request. Where a comment
// says "measured", it means exactly that and not "documented" — the startup
// endpoints are what the setup wizard happens to call in the version we pin,
// not a contract, which is why every step here degrades to instructions rather
// than to a failure.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The endpoints, in the order the wizard calls them.
const (
	pathSystemInfoPublic     = "/System/Info/Public"
	pathStartupConfiguration = "/Startup/Configuration"
	pathStartupUser          = "/Startup/User"
	pathStartupRemoteAccess  = "/Startup/RemoteAccess"
	pathStartupComplete      = "/Startup/Complete"
	pathAuthenticateByName   = "/Users/AuthenticateByName"
	pathVirtualFolders       = "/Library/VirtualFolders"
	pathAuthKeys             = "/Auth/Keys"
)

// startupPrefix is the family the guard protects. Everything under it
// reconfigures the server itself, and none of it may run against one that has
// finished its wizard — Jellyfin will not refuse it, so this package must.
const startupPrefix = "/Startup/"

// maxProvisionBody caps a response before it is decoded. The largest of these
// is the key listing, which is a few hundred bytes per key; a megabyte is a
// ceiling for a proxy answering with something that is not Jellyfin at all,
// not a budget.
const maxProvisionBody = 1 << 20

// authClient identifies curator to Jellyfin in the Authorization header that
// POST /Users/AuthenticateByName requires. Without the header that call
// answers 400 — measured — which is not the same thing as a wrong password.
const authClient = "curator"

// authDeviceID is the DeviceId Jellyfin files the resulting session under. It
// is a constant rather than a per-install identifier because the session is
// used once, to mint an API key, and then dropped: curator never logs in as a
// human again (T64, "do not store the user's Jellyfin password"). Two curator
// installs pointing at one Jellyfin therefore share one device row, which
// costs nothing that is ever read.
const authDeviceID = "curator"

// KeyAppName is the AppName curator's own API key is filed under, and the
// string MintKey matches on when it reads the key back.
const KeyAppName = "curator"

// ErrAlreadyConfigured reports that the server has finished its startup wizard
// and this operation may not run against it.
//
// It is the whole point of the type. Measured on 10.10.7: a server reporting
// StartupWizardCompleted:true answered 204 to
// POST /Startup/User {"Name":"attacker","Password":"x"} carrying a valid API
// key, renamed the admin account and changed its password — same user Id, and
// the original credentials answered 401 afterwards. Unauthenticated the same
// call is 401. So a configured Jellyfin does not close its setup endpoints to
// an authenticated caller, and the only thing between a household and being
// locked out of their own media server is this error.
var ErrAlreadyConfigured = errors.New("jellyfin has already completed its startup wizard")

// ErrBadCredentials reports that Jellyfin recognised the request and refused
// the username or password — a 401 from AuthenticateByName, and nothing else.
var ErrBadCredentials = errors.New("jellyfin rejected the username or password")

// ErrAuthRequestRejected reports a 400 from AuthenticateByName, which means the
// request itself was wrong rather than the credentials.
//
// Kept distinct from ErrBadCredentials deliberately. Measured: the call without
// an `Authorization: MediaBrowser …` header is 400, and a genuinely wrong
// password is 401. Telling someone their password is wrong when curator sent a
// malformed header is the worst available message, because the password is the
// one thing they will retype for ever.
var ErrAuthRequestRejected = errors.New("jellyfin refused the sign-in request itself, not the credentials")

// ErrUnreachable reports that nothing answered at all — the container is not up
// yet, the URL is wrong, or the network is.
var ErrUnreachable = errors.New("jellyfin could not be reached")

// ErrNotReady reports that Jellyfin is up and still loading.
//
// Measured, and it is not the same state as unreachable: a starting 10.10.7
// accepts the connection and answers 503 with the body "Jellyfin Server is
// loading. Please try again shortly." for some seconds before its first 200.
// Both mean "wait and ask again", so a caller polling for a container it just
// asked the user to start treats them alike — but when the wait finally times
// out they are different sentences to put on the screen. Nothing listening
// means the pasted command did not run; still loading means it did.
var ErrNotReady = errors.New("jellyfin is still starting up")

// ErrUnexpectedResponse reports that Jellyfin answered something this version
// does not understand: a status not in the measured table, or a body that does
// not decode.
//
// This is the degrade path and it is a required one. The startup endpoints are
// not a documented contract, so a later Jellyfin may answer differently; when
// it does, the caller must show the manual steps with the exact path to paste
// rather than fail. A provisioning flow that breaks on somebody else's release
// schedule and has no fallback is a support burden this project cannot carry.
var ErrUnexpectedResponse = errors.New("jellyfin answered something curator does not understand")

// Consent is a caller's explicit opt-in to touching a server that has already
// finished its wizard.
//
// It is an enum rather than a bool because the zero value has to be the safe
// one and `true` must not compile into the permissive slot by accident. Only
// AddLibrary and MintKey take one at all: they are the two operations that are
// purely additive, and they are T66's adopt branch. Nothing under /Startup/
// takes a Consent, because nothing under /Startup/ may ever run against a
// configured server for any reason.
type Consent int

const (
	// OnlyIfUnconfigured is the zero value: refuse a server whose wizard is
	// complete.
	OnlyIfUnconfigured Consent = iota
	// AdoptConfigured says the user has been shown that this server is already
	// set up and has chosen to add to it anyway.
	AdoptConfigured
)

// ServerStatus is what /System/Info/Public says about a Jellyfin. It needs no
// credential, on a configured server as well as a fresh one, which is what
// makes it usable as a guard.
type ServerStatus struct {
	Version         string
	ServerID        string
	WizardCompleted bool
}

// Session is one signed-in user, held only long enough to mint an API key.
//
// The password that produced it is not stored anywhere and neither is this:
// what gets persisted is the key, through phase 7's secret machinery
// (docs/decisions.md D28).
type Session struct {
	Token    string
	UserID   string
	ServerID string
}

// StartupConfiguration is the locale triple the wizard's first screen sets. A
// zero field means "keep what the server already answered", which is why
// Configure reads before it writes.
type StartupConfiguration struct {
	UICulture                 string
	MetadataCountryCode       string
	PreferredMetadataLanguage string
}

// Provisioner sets up a Jellyfin that curator brought up.
//
// It is a separate type from Client and not a set of methods on it, and that
// separation is the deliverable rather than a matter of taste: the importer and
// the poller hold a *Client, so no amount of later editing can put a call that
// reconfigures a media server within reach of a background tick. Only the setup
// flow constructs this one.
type Provisioner struct {
	baseURL string
	// version is curator's, for the Authorization header Jellyfin records
	// against the session. It is passed in rather than declared here because
	// cmd/curator's Version is the one copy of that string in the repo — the
	// same reason internal/api's Settings takes it as a field.
	version string
	http    *http.Client
	log     *slog.Logger
}

// NewProvisioner returns a Provisioner for the Jellyfin at baseURL, empty
// meaning DefaultBaseURL.
//
// It takes no API key, and that is the point: at the moment it is constructed
// there is no key, because minting one is the last thing it does. httpClient
// and log may both be nil.
func NewProvisioner(baseURL, version string, httpClient *http.Client, log *slog.Logger) *Provisioner {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout}
	}
	if log == nil {
		log = slog.Default()
	}
	return &Provisioner{
		// Trimmed here for the same reason New trims it: every path below
		// carries its own leading "/", and a doubled slash is a 404 from a
		// server that would otherwise have worked.
		baseURL: strings.TrimSuffix(strings.TrimSpace(baseURL), "/"),
		version: version,
		http:    httpClient,
		log:     log,
	}
}

// Status reads /System/Info/Public.
//
// It is also the guard, and it is deliberately cheap and unauthenticated so
// that being the guard costs nothing worth optimising away.
func (p *Provisioner) Status(ctx context.Context) (ServerStatus, error) {
	status, body, err := p.do(ctx, http.MethodGet, pathSystemInfoPublic, nil, "", nil)
	if err != nil {
		return ServerStatus{}, err
	}
	if status != http.StatusOK {
		return ServerStatus{}, unexpectedStatus(pathSystemInfoPublic, status, body)
	}

	var decoded struct {
		Version                string `json:"Version"`
		ID                     string `json:"Id"`
		StartupWizardCompleted bool   `json:"StartupWizardCompleted"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return ServerStatus{}, unexpectedBody(pathSystemInfoPublic, err)
	}
	// Something that answers 200 with JSON and carries no Version is not a
	// Jellyfin — a reverse proxy, curator itself, a router's admin page. Saying
	// so here means the next call does not write into it.
	if decoded.Version == "" {
		return ServerStatus{}, fmt.Errorf(
			"jellyfin provision: %s answered 200 with no Version, so this is not a jellyfin: %w",
			pathSystemInfoPublic, ErrUnexpectedResponse)
	}

	return ServerStatus{
		Version:         decoded.Version,
		ServerID:        decoded.ID,
		WizardCompleted: decoded.StartupWizardCompleted,
	}, nil
}

// requireUnconfigured is the guard every /Startup/ method calls first.
//
// It re-reads the server every time and caches nothing, because the failure
// being defended against is precisely a server that changed underneath a
// half-finished flow — the user finishing the wizard in another tab while
// curator's screen is still open is the ordinary way that happens, not an
// exotic one.
func (p *Provisioner) requireUnconfigured(ctx context.Context) error {
	status, err := p.Status(ctx)
	if err != nil {
		return err
	}
	if status.WizardCompleted {
		return fmt.Errorf("jellyfin provision: jellyfin %s at %s is already set up: %w",
			status.Version, p.baseURL, ErrAlreadyConfigured)
	}
	return nil
}

// permit is the guard for the two additive operations. An explicit
// AdoptConfigured skips the read entirely: the caller has already been told
// what this server is and has answered for it.
func (p *Provisioner) permit(ctx context.Context, consent Consent) error {
	if consent == AdoptConfigured {
		return nil
	}
	return p.requireUnconfigured(ctx)
}

// Configure sets the locale triple, reading the server's current answer first
// so that an unset field in want keeps whatever Jellyfin already had rather
// than blanking it.
func (p *Provisioner) Configure(ctx context.Context, want StartupConfiguration) error {
	if err := p.requireUnconfigured(ctx); err != nil {
		return err
	}

	status, body, err := p.do(ctx, http.MethodGet, pathStartupConfiguration, nil, "", nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return unexpectedStatus(pathStartupConfiguration, status, body)
	}

	// The wire shape and the Go shape are the same three fields, but they are
	// declared separately: the JSON tags are Jellyfin's spelling and the struct
	// above is curator's, and collapsing them would make a rename upstream a
	// silent behaviour change here.
	var current struct {
		UICulture                 string `json:"UICulture"`
		MetadataCountryCode       string `json:"MetadataCountryCode"`
		PreferredMetadataLanguage string `json:"PreferredMetadataLanguage"`
	}
	if err := json.Unmarshal(body, &current); err != nil {
		return unexpectedBody(pathStartupConfiguration, err)
	}

	if want.UICulture != "" {
		current.UICulture = want.UICulture
	}
	if want.MetadataCountryCode != "" {
		current.MetadataCountryCode = want.MetadataCountryCode
	}
	if want.PreferredMetadataLanguage != "" {
		current.PreferredMetadataLanguage = want.PreferredMetadataLanguage
	}

	status, body, err = p.do(ctx, http.MethodPost, pathStartupConfiguration, nil, "", current)
	if err != nil {
		return err
	}
	return expectNoContent(pathStartupConfiguration, status, body)
}

// CreateAdmin names the first user and sets its password.
//
// The GET before the POST is not decoration: it is the last chance to find out
// that this server's startup API is not the one that was measured, and finding
// that out before the write means the server is still visibly unfinished rather
// than half-configured and claiming otherwise.
func (p *Provisioner) CreateAdmin(ctx context.Context, name, password string) error {
	if err := p.requireUnconfigured(ctx); err != nil {
		return err
	}

	status, body, err := p.do(ctx, http.MethodGet, pathStartupUser, nil, "", nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return unexpectedStatus(pathStartupUser, status, body)
	}
	var current struct {
		Name string `json:"Name"`
	}
	if err := json.Unmarshal(body, &current); err != nil {
		return unexpectedBody(pathStartupUser, err)
	}

	status, body, err = p.do(ctx, http.MethodPost, pathStartupUser, nil, "", map[string]string{
		"Name":     name,
		"Password": password,
	})
	if err != nil {
		return err
	}
	return expectNoContent(pathStartupUser, status, body)
}

// EnableRemoteAccess opens the server to the LAN and leaves UPnP alone.
//
// Automatic port mapping is off deliberately: this is a LAN product with no
// TLS (docs/decisions.md D25), and a bundle that punched a hole in the
// household's router on the user's behalf would be making a decision nobody
// asked it to make.
func (p *Provisioner) EnableRemoteAccess(ctx context.Context) error {
	if err := p.requireUnconfigured(ctx); err != nil {
		return err
	}
	status, body, err := p.do(ctx, http.MethodPost, pathStartupRemoteAccess, nil, "", map[string]bool{
		"EnableRemoteAccess":         true,
		"EnableAutomaticPortMapping": false,
	})
	if err != nil {
		return err
	}
	return expectNoContent(pathStartupRemoteAccess, status, body)
}

// CompleteSetup closes the wizard, after which /System/Info/Public reports
// StartupWizardCompleted:true and every method above starts refusing.
//
// It is called last for that reason: authenticating, creating the library and
// minting the key all work before it, so if any of them fails the server is
// left obviously unfinished instead of finished and wrong.
func (p *Provisioner) CompleteSetup(ctx context.Context) error {
	if err := p.requireUnconfigured(ctx); err != nil {
		return err
	}
	status, body, err := p.do(ctx, http.MethodPost, pathStartupComplete, nil, "", nil)
	if err != nil {
		return err
	}
	return expectNoContent(pathStartupComplete, status, body)
}

// Authenticate signs in as a user and returns the session.
//
// No guard: this is not a /Startup/ endpoint, it writes nothing, and the adopt
// branch needs it against a server that is already configured.
func (p *Provisioner) Authenticate(ctx context.Context, username, password string) (Session, error) {
	// Pw, not Password. Jellyfin's AuthenticateByName has taken both over the
	// years and 10.10.7 takes this one.
	status, body, err := p.do(ctx, http.MethodPost, pathAuthenticateByName, nil, "", map[string]string{
		"Username": username,
		"Pw":       password,
	})
	if err != nil {
		return Session{}, err
	}

	switch status {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return Session{}, fmt.Errorf("jellyfin provision: %s: %w", pathAuthenticateByName, ErrBadCredentials)
	case http.StatusBadRequest:
		// Measured: this is what a missing or malformed
		// `Authorization: MediaBrowser …` header produces. It is curator's
		// fault or a Jellyfin that changed, and it is never the password.
		return Session{}, fmt.Errorf("jellyfin provision: %s answered 400 (%s): %w",
			pathAuthenticateByName, snippet(body), ErrAuthRequestRejected)
	default:
		return Session{}, unexpectedStatus(pathAuthenticateByName, status, body)
	}

	var decoded struct {
		AccessToken string `json:"AccessToken"`
		ServerID    string `json:"ServerId"`
		User        struct {
			ID string `json:"Id"`
		} `json:"User"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return Session{}, unexpectedBody(pathAuthenticateByName, err)
	}
	if decoded.AccessToken == "" {
		return Session{}, fmt.Errorf("jellyfin provision: %s answered 200 with no AccessToken: %w",
			pathAuthenticateByName, ErrUnexpectedResponse)
	}

	return Session{
		Token:    decoded.AccessToken,
		UserID:   decoded.User.ID,
		ServerID: decoded.ServerID,
	}, nil
}

// AddLibrary creates a Movies library pointing at libraryPath and then proves
// it exists.
//
// libraryPath is the path as JELLYFIN sees it, which inside compose is the
// shared volume both services mount. Getting those two mounts to agree is the
// step people fail silently when they do this by hand — the library scans,
// nothing appears, and no error is produced anywhere — and it is the whole
// reason this method exists rather than a paragraph in a README.
func (p *Provisioner) AddLibrary(ctx context.Context, session Session, name, libraryPath string, consent Consent) error {
	if err := p.permit(ctx, consent); err != nil {
		return err
	}

	query := url.Values{
		"name":           {name},
		"collectionType": {"movies"},
		// Measured: created with refreshLibrary=false the folder came back in
		// the listing with no ItemId and no LibraryOptions key at all until a
		// refresh materialised it. The verification below depends on this.
		"refreshLibrary": {"true"},
	}
	// PathInfos and nothing else. An unspecified boolean in LibraryOptions
	// deserialises to false, so a "fuller" body invents a configuration
	// Jellyfin's own UI never produces — and the field people reach for,
	// EnableInternetProviders, reads false in the listing while metadata
	// fetching runs anyway: options.xml omits it and <TypeOptions /> is empty,
	// which means defaults apply. Sending more here is how ProviderIds.Tmdb
	// gets switched off by accident, which would break Open in Jellyfin for
	// every film, silently, with the link still rendering (D32).
	body := map[string]any{
		"LibraryOptions": map[string]any{
			"PathInfos": []map[string]string{{"Path": libraryPath}},
		},
	}

	status, respBody, err := p.do(ctx, http.MethodPost, pathVirtualFolders, query, session.Token, body)
	if err != nil {
		return err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return fmt.Errorf("jellyfin provision: %s answered %d: %w", pathVirtualFolders, status, ErrUnauthorized)
	}
	if err := expectNoContent(pathVirtualFolders, status, respBody); err != nil {
		return err
	}

	// A 204 is not proof, so the listing is re-read and the path looked for.
	folders, err := p.libraries(ctx, session)
	if err != nil {
		return err
	}
	for _, folder := range folders {
		for _, location := range folder.Locations {
			if samePath(location, libraryPath) {
				return nil
			}
		}
	}
	return fmt.Errorf(
		"jellyfin provision: %s answered %d but %q is in no library's Locations afterwards: %w",
		pathVirtualFolders, status, libraryPath, ErrUnexpectedResponse)
}

// virtualFolder is a library as the listing reports it, cut down to what is
// used. Widening this is how the narrowness rule gets lost one convenient
// field at a time.
type virtualFolder struct {
	Name      string   `json:"Name"`
	Locations []string `json:"Locations"`
}

// libraries reads the library listing.
func (p *Provisioner) libraries(ctx context.Context, session Session) ([]virtualFolder, error) {
	status, body, err := p.do(ctx, http.MethodGet, pathVirtualFolders, nil, session.Token, nil)
	if err != nil {
		return nil, err
	}
	switch status {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("jellyfin provision: %s answered %d: %w", pathVirtualFolders, status, ErrUnauthorized)
	default:
		return nil, unexpectedStatus(pathVirtualFolders, status, body)
	}

	var folders []virtualFolder
	if err := json.Unmarshal(body, &folders); err != nil {
		return nil, unexpectedBody(pathVirtualFolders, err)
	}
	return folders, nil
}

// samePath compares a location Jellyfin echoed back with the one curator sent.
//
// Jellyfin returns the string it was given, so the only difference worth
// tolerating is a trailing slash. Deliberately not filepath.Clean: this is a
// path inside somebody else's container, and curator's own OS has no say in
// what it means.
func samePath(a, b string) bool {
	trim := func(s string) string {
		s = strings.TrimSpace(s)
		if s == "/" {
			return s
		}
		return strings.TrimSuffix(s, "/")
	}
	return trim(a) == trim(b)
}

// authKey is one entry of GET /Auth/Keys.
type authKey struct {
	AccessToken string `json:"AccessToken"`
	AppName     string `json:"AppName"`
	DateCreated string `json:"DateCreated"`
}

// MintKey returns curator's own API key for this server, creating it if there
// is not one already.
//
// The listing is read BEFORE anything is posted, because POST /Auth/Keys is not
// idempotent: measured, two posts with app=curator produced two keys both named
// curator, both with Id 0, and nothing distinguishes them afterwards. A retried
// provision would otherwise litter the user's server with credentials it will
// never use and cannot tell apart.
//
// The key is never logged, at any level. It goes to phase 7's secret machinery,
// which encrypts it at rest and never reads it back across the API (D28).
func (p *Provisioner) MintKey(ctx context.Context, session Session, app string, consent Consent) (string, error) {
	if err := p.permit(ctx, consent); err != nil {
		return "", err
	}

	if existing, ok, err := p.findKey(ctx, session, app); err != nil {
		return "", err
	} else if ok {
		p.log.Info("reusing the jellyfin api key curator already minted", "app", app)
		return existing, nil
	}

	status, body, err := p.do(ctx, http.MethodPost, pathAuthKeys, url.Values{"app": {app}}, session.Token, nil)
	if err != nil {
		return "", err
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		// /Auth/Keys needs a credential even while the wizard is open —
		// unauthenticated it is 401, measured — so this is the one step of the
		// sequence that can fail on the token rather than on the server.
		return "", fmt.Errorf("jellyfin provision: %s answered %d: %w", pathAuthKeys, status, ErrUnauthorized)
	}
	// 204 with an empty body is what 10.10.7 sends: the key is not in the
	// response and has to be read back.
	if err := expectNoContent(pathAuthKeys, status, body); err != nil {
		return "", err
	}

	key, ok, err := p.findKey(ctx, session, app)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf(
			"jellyfin provision: %s accepted the key named %q and %s does not list it afterwards: %w",
			pathAuthKeys, app, pathAuthKeys, ErrUnexpectedResponse)
	}
	return key, nil
}

// findKey reads the key listing and picks the one named app.
//
// More than one match is a real state rather than a defensive branch — see
// MintKey — so the newest by DateCreated wins and the fact is logged, because
// the alternative is a user whose server quietly accumulates credentials with
// nothing anywhere saying so.
func (p *Provisioner) findKey(ctx context.Context, session Session, app string) (string, bool, error) {
	status, body, err := p.do(ctx, http.MethodGet, pathAuthKeys, nil, session.Token, nil)
	if err != nil {
		return "", false, err
	}
	switch status {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return "", false, fmt.Errorf("jellyfin provision: %s answered %d: %w", pathAuthKeys, status, ErrUnauthorized)
	default:
		return "", false, unexpectedStatus(pathAuthKeys, status, body)
	}

	var decoded struct {
		Items []authKey `json:"Items"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", false, unexpectedBody(pathAuthKeys, err)
	}

	var (
		best    authKey
		bestAt  time.Time
		matches int
	)
	for _, item := range decoded.Items {
		if item.AppName != app || item.AccessToken == "" {
			continue
		}
		matches++
		// An unparseable DateCreated sorts as the zero time, so the first match
		// stands rather than the loop picking arbitrarily.
		at, _ := time.Parse(time.RFC3339, item.DateCreated)
		if matches == 1 || at.After(bestAt) {
			best, bestAt = item, at
		}
	}
	if matches == 0 {
		return "", false, nil
	}
	if matches > 1 {
		p.log.Warn("jellyfin holds more than one api key with this name; using the newest",
			"app", app, "count", matches, "created", best.DateCreated)
	}
	return best.AccessToken, true, nil
}

// --- the request, and the two errors every step maps onto -------------------

// do performs one provisioning request and returns the status and the body.
//
// token is X-Emby-Token and is omitted when empty: steps 1 to 5 of the measured
// sequence need no credential at all while the wizard is open, and sending one
// there would be inventing a requirement.
func (p *Provisioner) do(ctx context.Context, method, endpoint string, query url.Values, token string, body any) (int, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("jellyfin provision: encoding the body for %s: %w", endpoint, err)
		}
		payload = bytes.NewReader(encoded)
	}

	target := p.baseURL + endpoint
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, payload)
	if err != nil {
		return 0, nil, fmt.Errorf("jellyfin provision: build request for %s: %w", endpoint, err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set(tokenHeader, token)
	}
	if endpoint == pathAuthenticateByName {
		req.Header.Set("Authorization", p.authorization())
	}

	resp, err := p.http.Do(req)
	if err != nil {
		return 0, nil, unreachable{fmt.Errorf("jellyfin provision: %s %s: calling jellyfin at %s: %w",
			method, endpoint, p.baseURL, err)}
	}
	defer resp.Body.Close()

	// Capped, then drained, for the reason RefreshLibrary gives: the success
	// case is often no body at all and the failure case behind an unwell proxy
	// is a whole HTML page. Draining keeps the connection reusable.
	read, _ := io.ReadAll(io.LimitReader(resp.Body, maxProvisionBody))
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, read, nil
}

// authorization is the header AuthenticateByName requires. Without it that call
// is 400 — measured — which is why this is built rather than optional.
func (p *Provisioner) authorization() string {
	version := p.version
	if strings.TrimSpace(version) == "" {
		version = "unknown"
	}
	return fmt.Sprintf(`MediaBrowser Client=%q, Device=%q, DeviceId=%q, Version=%q`,
		authClient, authClient, authDeviceID, version)
}

// unreachable carries ErrUnreachable alongside the transport error rather than
// instead of it, so that both errors.Is(err, ErrUnreachable) and
// errors.Is(err, context.DeadlineExceeded) answer truthfully. errors.Join would
// do the same and put a newline in the message.
type unreachable struct{ err error }

func (u unreachable) Error() string   { return u.err.Error() }
func (u unreachable) Unwrap() []error { return []error{u.err, ErrUnreachable} }

// expectNoContent accepts what the measured sequence answers to a write.
func expectNoContent(endpoint string, status int, body []byte) error {
	// 204 is what 10.10.7 sends for every write in the table. 200 is accepted
	// for the same reason RefreshLibrary accepts it — older versions and some
	// proxies send it, and the distinction carries nothing we act on.
	if status == http.StatusNoContent || status == http.StatusOK {
		return nil
	}
	return unexpectedStatus(endpoint, status, body)
}

// unexpectedStatus is the degrade path for a status not in the measured table,
// with one status carved out of it.
func unexpectedStatus(endpoint string, status int, body []byte) error {
	// 503 is a Jellyfin that is loading rather than a Jellyfin that is wrong,
	// and it is the answer for most of the measured 17.6 s cold start. Sending
	// it down the degrade path would put "curator does not understand this
	// server, here are the manual steps" on the screen for the entire time the
	// container the user was just told to start is starting.
	if status == http.StatusServiceUnavailable {
		return fmt.Errorf("jellyfin provision: %s answered 503 (%s): %w",
			endpoint, snippet(body), ErrNotReady)
	}
	return fmt.Errorf("jellyfin provision: %s answered %d %s (%s): %w",
		endpoint, status, http.StatusText(status), snippet(body), ErrUnexpectedResponse)
}

// unexpectedBody is the degrade path for a body that does not decode.
func unexpectedBody(endpoint string, err error) error {
	return fmt.Errorf("jellyfin provision: decoding the answer from %s: %v: %w",
		endpoint, err, ErrUnexpectedResponse)
}
