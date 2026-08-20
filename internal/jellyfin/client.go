// Package jellyfin asks Jellyfin to rescan its library, looks up one item, and
// sets up a server curator brought up itself.
//
// The one on the Pi is 10.10.7 at 192.168.1.26:8096, reached as
// http://jellyfin:8096 from inside Docker. An unset key disables both calls
// rather than failing startup (docs/decisions.md D15).
//
// The narrowness is the design, and phase 8 amended it by exactly one method.
// There are no per-item refreshes, no user or session endpoints and no playback
// control, for the same reason internal/qbit cannot delete or pause a torrent:
// a method that does not exist cannot be called by mistake against a media
// server the household is watching. FindMovie is the lookup T45 needed and
// FindSeries is the same query for the other media type phase 11 added; both
// are read-only, and if a later phase needs more, it can add exactly what it
// needs and no more.
//
// Writing exists, and it is confined to one type. Provisioner, in provision.go,
// completes a startup wizard, creates a library and mints an API key, because
// phase 9 ships Jellyfin beside curator and the alternative is a stranger
// pasting a key between two web UIs and getting two sets of Docker mounts to
// agree unaided (docs/decisions.md D34). Only the setup flow constructs one:
// Client is what the importer and the poller hold, and it still cannot write.
// So the guarantee is no longer "the package has no such method" but "the type
// that has it is not reachable from a background tick", which is weaker and is
// therefore paid for rather than asserted — every method that touches
// /Startup/ re-reads /System/Info/Public first and refuses a server whose
// wizard is complete. That guard is load-bearing because Jellyfin has none of
// its own: measured on 10.10.7, POST /Startup/User against a fully configured
// server carrying a valid API key answered 204 and changed the admin's name and
// password.
//
// RefreshLibrary returns an error, deliberately. The guarantee that a refresh
// can never fail an import or a poll tick is implemented one layer up, at
// internal/importer's seam, where swallowing it is a considered decision made
// once. A client that hid its own failures could not be tested, and its live
// test could not fail on a bad status.
package jellyfin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultBaseURL is where Jellyfin listens when curator runs beside it. On the
// Pi it is 192.168.1.26:8096 from a laptop and http://jellyfin:8096 inside
// Docker; this default is for running on a laptop, which is where it gets typed
// most.
const DefaultBaseURL = "http://127.0.0.1:8096"

// requestTimeout bounds a refresh. Jellyfin answers one immediately — it queues
// the scan rather than performing it — so this is a ceiling for a wedged
// instance, not a budget.
const requestTimeout = 15 * time.Second

// lookupTimeout bounds FindMovie, and it is much shorter than requestTimeout
// because of who is waiting.
//
// A refresh is fired by a poller with nobody watching, so fifteen seconds costs
// nothing. This one is inside a page load: it decides whether the Open in
// Jellyfin link is a deep link or a search link, and both are useful, so a
// Jellyfin that is switched off must cost a moment rather than a page. Measured
// against the real 10.10.7 over a LAN: 5.5 ms.
const lookupTimeout = 2 * time.Second

// pathLibraryRefresh queues a scan of every library. Jellyfin answers 204 and
// does the work in the background.
const pathLibraryRefresh = "/Library/Refresh"

// pathItems is the query behind Open in Jellyfin.
const pathItems = "/Items"

// tokenHeader is how Jellyfin takes an API key. It can also be passed as an
// api_key query parameter, which this package deliberately does not use: a
// query string ends up in *url.Error messages, in access logs and in any proxy
// in between, and a header does not.
const tokenHeader = "X-Emby-Token"

// ErrUnauthorized reports that Jellyfin refused the API key rather than the
// request.
//
// It is separated from every other failure because the two want opposite
// handling: an unreachable Jellyfin is worth retrying on the next tick, and a
// revoked key is worth reporting once and not hammering. Test with errors.Is.
var ErrUnauthorized = errors.New("jellyfin rejected the API key")

// Client talks to one Jellyfin. The zero value is not usable; call New.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
}

// New returns a client for the Jellyfin at baseURL, empty meaning
// DefaultBaseURL. The base is a parameter so an httptest.Server can stand in,
// which is how every test here runs.
//
// httpClient is injected so callers share one connection pool; a nil one gets a
// default with a timeout, since http.DefaultClient has none and would hang
// forever.
func New(baseURL, apiKey string, httpClient *http.Client) *Client {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout}
	}
	return &Client{
		// Trailing slash trimmed here rather than at every use: pathLibraryRefresh
		// carries its own leading "/", and "…8096//Library/Refresh" is a 404 from
		// a server that would otherwise have worked.
		baseURL: strings.TrimSuffix(strings.TrimSpace(baseURL), "/"),
		apiKey:  apiKey,
		http:    httpClient,
	}
}

// RefreshLibrary asks Jellyfin to rescan every library.
//
// It returns as soon as the scan is queued — 204 No Content — not when the scan
// finishes, so a caller learns that Jellyfin accepted the request and nothing
// more. That is all an importer needs: the file is already hardlinked into the
// library and Jellyfin would find it on its own schedule regardless.
func (c *Client) RefreshLibrary(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	endpoint := c.baseURL + pathLibraryRefresh
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return fmt.Errorf("jellyfin refresh: build request: %w", err)
	}
	req.Header.Set(tokenHeader, c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("jellyfin refresh: calling jellyfin at %s: %w", c.baseURL, err)
	}
	defer resp.Body.Close()

	// Capped, then drained: the success case has no body at all, and the failure
	// case behind an unwell reverse proxy is a whole HTML error page that has no
	// business in an error string. Draining the rest keeps the connection
	// reusable.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	_, _ = io.Copy(io.Discard, resp.Body)

	switch resp.StatusCode {
	case http.StatusNoContent, http.StatusOK:
		// 204 is what 10.10.7 sends. 200 is accepted because older and
		// differently-proxied versions send it, and the distinction carries no
		// information we act on.
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		// Both mean the token rather than the request: 401 for a key Jellyfin
		// does not recognise, 403 for one whose user is not permitted to trigger
		// a scan. Neither is fixed by trying again.
		return fmt.Errorf("jellyfin refresh: jellyfin answered %d %s (%s): %w",
			resp.StatusCode, http.StatusText(resp.StatusCode), snippet(body), ErrUnauthorized)
	default:
		return fmt.Errorf("jellyfin refresh: jellyfin answered %d %s: %s",
			resp.StatusCode, http.StatusText(resp.StatusCode), snippet(body))
	}
}

// snippet renders the front of a body for an error message, capped and
// flattened to one line.
func snippet(body []byte) string {
	flat := strings.Join(strings.Fields(string(body)), " ")
	if flat == "" {
		return "no body"
	}
	if len(flat) > 256 {
		flat = flat[:256] + "…"
	}
	return strconv.Quote(flat)
}

// --- the one lookup, and the links it is for --------------------------------

// ErrNotFound reports that Jellyfin answered and does not have this film or
// this show.
//
// Separate from every transport failure for the same reason ErrUnauthorized is:
// the caller does the same thing with it either way — falls back to a search
// link — but only one of the two is worth a line in the log.
//
// One sentinel for both lookups, because every caller does the same thing with
// it. The wording says "item" rather than "movie" now that a show can be the
// thing that is missing.
var ErrNotFound = errors.New("jellyfin has no item with this tmdb id")

// maxItemsBody caps the lookup's response.
//
// Narrowed by year it is 542 bytes, measured. Unnarrowed — which happens only
// for a film TMDB has no release date for — it is the whole movie library at
// about 1.2 KB per film, so this is roughly a three-thousand-film ceiling.
// Past it the JSON is truncated, the decode fails, and the caller falls back to
// the search link, which is the same thing it does for every other miss.
const maxItemsBody = 4 << 20

// The two item types this package looks up, spelled as Jellyfin's own
// IncludeItemTypes values.
//
// Series and not Episode: a show's page in Jellyfin is the series, which is
// also the row curator holds and the thing a TMDB tv id names. Asking for
// episodes would answer hundreds of items that all carry the series' provider
// ids and none of which is the page anybody wants to open.
const (
	itemTypeMovie  = "Movie"
	itemTypeSeries = "Series"
)

// Item is a film or a show as Jellyfin holds it: enough to build a link to it
// and nothing else. Widening this is how the narrowness rule above gets lost
// one convenient field at a time.
type Item struct {
	ID       string
	ServerID string
}

// itemsResponse is the shape of GET /Items, cut down to what is used.
type itemsResponse struct {
	Items []struct {
		ID          string            `json:"Id"`
		ServerID    string            `json:"ServerId"`
		ProviderIDs map[string]string `json:"ProviderIds"`
	} `json:"Items"`
}

// providerTMDB is the key Jellyfin files a TMDB id under. Compared case-
// insensitively at the point of use: 10.10.7 writes "Tmdb", and a version that
// wrote "TMDB" would otherwise silently stop finding anything.
const providerTMDB = "Tmdb"

// FindMovie returns the Jellyfin item for a film, found by its TMDB id.
//
// **The TMDB id and not the path, and that is a measured decision rather than a
// preference** (docs/decisions.md D32). Jellyfin's /Items accepts a Path
// parameter and silently ignores it — a path that exists and a path that does
// not both answer the whole library — and it ignores AnyProviderIdEquals the
// same way, while years= and searchTerm= filter perfectly well. So there is no
// server-side identity filter to use, and the match happens here.
//
// The path would be the wrong key even if it worked: Jellyfin sees its own bind
// mount (/movies) where curator sees /media/storage/media/movies, and Jellyfin's
// Path is the file where curator's library_path is the folder. Every TMDB id is
// already on both sides, it survives a re-import at a different quality, and it
// steps around the ` - ` → `:` folder-title trap that a name match walks into.
//
// year narrows the query server-side to a few hundred bytes and is safe to
// narrow with: both sides take the year from TMDB, and all 18 films on the real
// 10.10.7 had a ProductionYear equal to TMDB's release year, checked film by
// film. A zero year skips the narrowing rather than the query.
func (c *Client) FindMovie(ctx context.Context, tmdbID, year int) (Item, error) {
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	return c.findItem(ctx, itemTypeMovie, tmdbID, year)
}

// FindSeries returns the Jellyfin item for a show, found by its TMDB **tv** id.
//
// It is FindMovie with one parameter changed, and that parameter is not a
// detail: `IncludeItemTypes` is the only thing separating the two id spaces.
// TMDB numbers films and shows independently, so 1399 is a film *and* a show —
// asking for a Movie with a tv id can therefore land on an unrelated film with
// the same number rather than merely miss. Every other rule here is D32's and
// is unchanged: the match is client-side and case-insensitive on
// ProviderIds.Tmdb, because Jellyfin silently ignores both Path and
// AnyProviderIdEquals, and no userId is sent, because a user-scoped library
// shrinks.
//
// **The year narrows, and then a miss asks again without it.** For a film D32
// checked all 18 on the real server and every ProductionYear equalled TMDB's
// release year, which is what makes the narrowing free there. Nothing
// equivalent has been measured for a show, and a show has more than one year to
// be wrong about — Jellyfin's Series.ProductionYear is the FIRST AIR year,
// while a folder on disk may be named for the season somebody has. A wrong
// narrowing would be exactly the failure this method exists to remove: a
// permanent miss, falling back to a search link, with nothing anywhere saying
// so. One extra request on a miss is the cheap end of that trade, and it is
// inside the one lookupTimeout rather than a second one.
func (c *Client) FindSeries(ctx context.Context, tmdbID, year int) (Item, error) {
	ctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()

	item, err := c.findItem(ctx, itemTypeSeries, tmdbID, year)
	if year > 0 && errors.Is(err, ErrNotFound) {
		return c.findItem(ctx, itemTypeSeries, tmdbID, 0)
	}
	return item, err
}

// findItem is the one query behind both lookups.
//
// itemType is Jellyfin's own IncludeItemTypes value, and the error messages are
// derived from it rather than passed beside it: two strings that have to agree
// are two strings that can stop agreeing, and this one is what a reader greps
// for when a link is missing.
//
// It sets no deadline of its own. FindSeries makes up to two of these calls and
// a page is waiting on the pair, not on each.
func (c *Client) findItem(ctx context.Context, itemType string, tmdbID, year int) (Item, error) {
	what := "jellyfin find " + strings.ToLower(itemType)

	query := url.Values{
		"Recursive":        {"true"},
		"IncludeItemTypes": {itemType},
		// ProviderIds is the whole point of the request; the images are the
		// bulk of an item and nothing here draws one.
		"Fields":       {"ProviderIds"},
		"EnableImages": {"false"},
	}
	if year > 0 {
		query.Set("years", strconv.Itoa(year))
	}

	// No userId, deliberately. With one the library shrinks to what that user
	// may see — 18 items became 16 on the real server — and the link is being
	// built for whoever is holding the laptop, not for the API key's owner.
	endpoint := c.baseURL + pathItems + "?" + query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Item{}, fmt.Errorf("%s: build request: %w", what, err)
	}
	req.Header.Set(tokenHeader, c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return Item{}, fmt.Errorf("%s: calling jellyfin at %s: %w", what, c.baseURL, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return Item{}, fmt.Errorf("%s: jellyfin answered %d %s (%s): %w",
			what, resp.StatusCode, http.StatusText(resp.StatusCode), snippet(body), ErrUnauthorized)
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return Item{}, fmt.Errorf("%s: jellyfin answered %d %s: %s",
			what, resp.StatusCode, http.StatusText(resp.StatusCode), snippet(body))
	}

	var decoded itemsResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxItemsBody)).Decode(&decoded); err != nil {
		return Item{}, fmt.Errorf("%s: decoding the item list: %w", what, err)
	}

	want := strconv.Itoa(tmdbID)
	for _, item := range decoded.Items {
		for key, value := range item.ProviderIDs {
			if !strings.EqualFold(key, providerTMDB) || value != want {
				continue
			}
			// **The first match wins, and a TMDB id is not unique.** Iron Man is
			// in the real library twice under 1726 — once under /movies and once
			// under /media/downloads/complete. Either lands on that film, so a
			// tie-break here would be inventing a preference nobody expressed.
			return Item{ID: item.ID, ServerID: item.ServerID}, nil
		}
	}
	return Item{}, ErrNotFound
}

// WebItemURL is a film's own page in Jellyfin's web client.
//
// The shape is not guesswork: it is the string main.jellyfin.bundle.js builds
// for an item of type Movie on the real 10.10.7. The item carries its own
// ServerId, so a multi-server web client opens the right one.
//
// base is jellyfin_public_url — what a BROWSER can reach, which inside Docker is
// never the http://jellyfin:8096 curator itself uses.
func WebItemURL(base string, item Item) string {
	if strings.TrimSpace(base) == "" || item.ID == "" {
		return ""
	}
	link := webBase(base) + "#/details?id=" + url.QueryEscape(item.ID)
	if item.ServerID != "" {
		link += "&serverId=" + url.QueryEscape(item.ServerID)
	}
	return link
}

// WebSearchURL is the fallback every miss lands on: Jellyfin's own search,
// pre-filled with the title.
//
// It always lands somewhere useful and it can never 404, which is the whole
// argument for preferring it to a guessed item id.
func WebSearchURL(base, title string) string {
	if strings.TrimSpace(base) == "" || strings.TrimSpace(title) == "" {
		return ""
	}
	return webBase(base) + "#/search.html?query=" + url.QueryEscape(title)
}

// webBase turns a server URL into the web client's, trimming a trailing slash
// so that neither "…:8096" nor "…:8096/" produces a doubled one.
func webBase(base string) string {
	return strings.TrimSuffix(strings.TrimSpace(base), "/") + "/web/index.html"
}
