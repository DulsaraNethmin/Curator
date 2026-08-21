package indexer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const eztvIndexerName = "eztv"

// DefaultEZTVBaseURL is EZTV's API root, overridable via WithEZTVBaseURL so
// tests can point the indexer at an httptest.Server.
//
// It is eztvx.to and NOT eztv.re, which docs/architecture.md named from phase 2
// until this task: probed 2026-08-21, eztv.re answers 301 while eztvx.to
// answers 200. D12 is the standing lesson about an indexer's front door moving
// without anybody noticing, and TestEZTVLive is what makes this one fail loudly
// if it happens again.
//
// No Cloudflare, so no minter: this is plain JSON over the shared client, the
// same shape as YTS.
const DefaultEZTVBaseURL = "https://eztvx.to/api"

// eztvPageLimit is how many rows one page asks for, and 100 is a CEILING rather
// than a preference.
//
// Measured 2026-08-21: limit=300 answers `"limit":30` with thirty rows. A
// number above the cap is not clamped to the cap — it is discarded, and the
// default is used — so asking for more returns FEWER results. Do not raise
// this without re-probing.
const eztvPageLimit = 100

// eztvMaxPages bounds a whole search at three pages, which is 300 rows.
//
// This is a real cap on coverage and it is chosen rather than imposed, so the
// arithmetic behind it belongs here. Measured 2026-08-21 against the live API:
// one page is 87 KB in ~2.5 s, and a show's whole catalogue runs from Game of
// Thrones' 146 rows through Silo's 270 to The Simpsons' 2,425. Three pages
// therefore covers most shows completely and costs ~7.5 s of the 30 s
// SEARCH_TIMEOUT that is shared with three other indexers; twenty-five pages
// would cover The Simpsons and eat the whole budget.
//
// What it costs is the tail: rows arrive NEWEST FIRST, so a long-running show
// truncates to its most recent 300 and an old season falls off the end. That is
// also why this is not one page. Silo's page 1 is seasons 2 and 3 only —
// season 1 first appears on page 2 — so a one-page budget would answer a
// season-1 search with a hundred rows that narrow() then drops, reporting
// ok:true and contributing nothing.
const eztvMaxPages = 3

// eztvRequestTimeout bounds the WHOLE paged search, not one page. Three pages
// at the measured ~2.5 s is ~7.5 s, so this is roughly double the expected
// cost and still well inside phase 2's 30 s whole-search deadline — an EZTV
// that has gone slow must not spend a budget shared with three other sources.
const eztvRequestTimeout = 20 * time.Second

// eztvMaxResponse bounds the body one page may return. A real page is 87 KB
// (measured, 100 rows), so this is two orders of magnitude of headroom and
// exists only so a hung or hostile endpoint cannot make a search allocate
// without limit. Same reasoning as tpbMaxResponse.
const eztvMaxResponse = 8 << 20

// EZTV searches EZTV's television catalogue.
//
// It is the only indexer that does not have to read a release name to know
// which episode a row is: the API returns `season` and `episode` as fields, so
// SeasonTier gets stated values where TPB and 1337x can only offer
// parseSeasonEpisode's reading of the title. That is the whole reason it is
// here — D49's tiers are only as good as the season each release is believed
// to be.
//
// It is also the only indexer that cannot answer every query it is handed. EZTV
// is keyed by IMDb id, not by title, so a search with no id is not a search it
// can narrow — see Answers.
type EZTV struct {
	http    *http.Client
	baseURL string
}

// EZTVOption customises an EZTV indexer.
type EZTVOption func(*EZTV)

// WithEZTVBaseURL points the indexer at another API root, so tests can
// substitute an httptest.Server. A trailing slash is trimmed so paths join
// predictably.
func WithEZTVBaseURL(base string) EZTVOption {
	return func(e *EZTV) { e.baseURL = strings.TrimSuffix(base, "/") }
}

// NewEZTV returns an EZTV indexer fetching through httpClient. The HTTP client
// is injected so callers share one connection pool; a nil one gets a default
// with a timeout, since http.DefaultClient has none and would hang forever.
func NewEZTV(httpClient *http.Client, opts ...EZTVOption) *EZTV {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: eztvRequestTimeout}
	}
	e := &EZTV{http: httpClient, baseURL: DefaultEZTVBaseURL}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// Name implements Indexer.
func (e *EZTV) Name() string { return eztvIndexerName }

// Handles implements MediaCapable: EZTV is television and nothing else.
//
// It is YTS's declaration in the mirror. There is no film surface to query
// here, and a film search that reached it would come back as an empty slice
// with a nil error — which the aggregator would report as ok:true, count:0, the
// quiet lie that is indistinguishable from "nobody has uploaded it".
func (e *EZTV) Handles(media string) bool { return media == MediaTV }

// Answers implements QueryCapable: EZTV can only answer a query that carries an
// IMDb id.
//
// This is not tidiness, and the measurement is why. get-torrents with no
// imdb_id does not fail and does not return nothing — probed 2026-08-21, it
// answers HTTP 200 with `torrents_count: 1075625` and a page of the newest
// uploads across every show on the site. So a title-only television search
// would come back full of real, well-seeded, completely unrelated releases,
// which is worse than an error in every way: nothing on the screen would look
// wrong.
//
// Declining is also the honest answer rather than a workaround. EZTV has no
// keyword search to fall back to — the id IS the query — so there is no
// narrower request to make.
func (e *EZTV) Answers(q Query) bool { return eztvIMDbID(q.IMDBID) != "" }

// Search implements Indexer. Every returned Release already carries a magnet:
// the API ships magnet_url on the search response, so unlike 1337x there is no
// detail page to resolve later.
//
// An empty slice with a nil error means "EZTV does not have this show", which
// is a normal outcome and not a failure.
func (e *EZTV) Search(ctx context.Context, q Query) ([]Release, error) {
	id := eztvIMDbID(q.IMDBID)
	if id == "" {
		// Unreachable through the aggregator, which asks Answers first. Kept as
		// an error rather than an empty result for a direct caller: an empty
		// slice here would mean "EZTV does not have this show", and that is not
		// what happened.
		return nil, fmt.Errorf("eztv search: %q is not an imdb id", q.IMDBID)
	}

	ctx, cancel := context.WithTimeout(ctx, eztvRequestTimeout)
	defer cancel()

	var (
		out     []Release
		fetched int
	)
	for page := 1; page <= eztvMaxPages; page++ {
		body, err := e.page(ctx, id, page)
		if err != nil {
			if page == 1 {
				// Nothing was fetched, so there is nothing to salvage and this
				// is simply a failed search.
				return nil, err
			}
			// A later page failing keeps what the earlier ones returned, and
			// this is the same trade eztvMaxPages already makes: the deep tail
			// of a long catalogue is not guaranteed to arrive. Discarding a
			// hundred good rows because page three hiccuped would be the worse
			// answer, and the rows in hand are correct rather than partial
			// nonsense — they are simply fewer.
			break
		}

		fetched += len(body.Torrents)
		out = append(out, eztvReleases(body.Torrents)...)

		// A short page is the last page.
		if len(body.Torrents) < eztvPageLimit {
			break
		}
		// And so is one that has covered everything the API says exists. Read
		// as a stop condition only, never as "did we find anything" — an id
		// EZTV does not know answers 200 with a count and no `torrents` key at
		// all (measured), exactly like YTS's movie_count without movies.
		if body.TorrentsCount > 0 && fetched >= body.TorrentsCount {
			break
		}
	}
	return out, nil
}

// page fetches one page of results.
func (e *EZTV) page(ctx context.Context, imdbID string, page int) (*eztvResponse, error) {
	params := url.Values{}
	// Without the tt prefix. TMDB reports "tt14688458" and EZTV wants
	// "14688458"; the strip happens here, at this indexer's boundary, because
	// one source's URL format is not internal/tmdb's business.
	params.Set("imdb_id", imdbID)
	params.Set("limit", strconv.Itoa(eztvPageLimit))
	params.Set("page", strconv.Itoa(page))
	endpoint := e.baseURL + "/get-torrents?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("eztv search %s page %d: build request: %w", imdbID, page, err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := e.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("eztv search %s page %d: %w", imdbID, page, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("eztv search %s page %d: unexpected status %s: %s",
			imdbID, page, resp.Status, eztvBodySnippet(resp.Body))
	}

	var body eztvResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, eztvMaxResponse)).Decode(&body); err != nil {
		return nil, fmt.Errorf("eztv search %s page %d: decode response: %w", imdbID, page, err)
	}
	return &body, nil
}

// eztvResponse is one page of get-torrents.
//
// TorrentsCount is the site's total for this show and is used ONLY to stop
// paging early. It is deliberately not read as "did we find anything": an
// unknown id answers `{"torrents_count":0,...}` with no `torrents` key at all
// (measured 2026-08-21, imdb_id=0000000, HTTP 200), which is the same trap
// ytsData documents for movie_count. len(Torrents) is the only honest answer to
// that question.
type eztvResponse struct {
	TorrentsCount int           `json:"torrents_count"`
	Limit         int           `json:"limit"`
	Page          int           `json:"page"`
	Torrents      []eztvTorrent `json:"torrents"`
}

// eztvTorrent is one release.
//
// Season, Episode and SizeBytes arrive as JSON **strings** — "3", "8",
// "288220838" — while Seeds and Peers arrive as numbers, in the same object.
// The field names and types here come off a recorded response
// (testdata/eztv-silo.json), not off documentation.
type eztvTorrent struct {
	Title     string `json:"title"`    // "Silo S03E08 XviD-AFG EZTV"
	Filename  string `json:"filename"` // "Silo.S03E08.XviD-AFG[EZTVx.to].avi"
	MagnetURL string `json:"magnet_url"`
	Seeds     int    `json:"seeds"`

	SizeBytes eztvNumber `json:"size_bytes"`
	Season    eztvNumber `json:"season"`
	Episode   eztvNumber `json:"episode"`
}

// eztvNumber is an integer that decodes from either a JSON string or a JSON
// number.
//
// Every value measured is a string, so a plain int64 would fail to decode the
// whole page today — and the failure would be loud, which is fine. What it
// would not survive is EZTV quietly switching to numbers, and a size that
// arrives as one is not worth breaking every television search over. This
// accepts both and costs ten lines. ytsTorrent's comment warns about exactly
// this family of field and stops one step short of handling it.
type eztvNumber int64

func (n *eztvNumber) UnmarshalJSON(raw []byte) error {
	// A JSON null leaves the zero value, which for all three fields this type
	// serves already means "not stated".
	if string(raw) == "null" {
		return nil
	}
	s := strings.Trim(string(raw), `"`)
	if s == "" {
		return nil
	}
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("eztv: %s is not a number", raw)
	}
	*n = eztvNumber(v)
	return nil
}

// eztvReleases converts one page into releases, dropping any row with no usable
// magnet.
//
// A row without one cannot be acted on and EZTV has no detail page to resolve a
// magnet from later, so it is dropped rather than handed out with an empty
// Magnet — which elsewhere in this package means "not resolved yet". Same rule
// as ytsReleases and a missing info hash.
func eztvReleases(torrents []eztvTorrent) []Release {
	out := make([]Release, 0, len(torrents))
	for _, t := range torrents {
		magnet := strings.TrimSpace(t.MagnetURL)
		if InfoHash(magnet) == "" {
			continue
		}
		name := strings.TrimSpace(t.Title)
		if name == "" {
			name = strings.TrimSpace(t.Filename)
		}

		season, episode := eztvSeasonEpisode(name, t)
		out = append(out, Release{
			Title: name,
			// Deliberately 0, like every other television release. A show's
			// year is its first-air year and an episode's name carries its own
			// — see Query.searchYear for the measurement that settles it.
			Year: 0,
			// Read off the name, because EZTV states the episode but not the
			// resolution. parseQuality is the same reading TPB's rows get.
			Quality:   parseQuality(name),
			SizeBytes: int64(t.SizeBytes),
			Seeders:   t.Seeds,
			// Verbatim from the API, trackers and all. Nothing is stapled on
			// the way ytsMagnet and tpbMagnet have to, because EZTV publishes
			// a complete magnet rather than a bare info hash.
			Magnet:  magnet,
			Indexer: eztvIndexerName,
			Season:  season,
			Episode: episode,
		})
	}
	return out
}

// eztvSeasonEpisode prefers what EZTV STATES over what the name can be read to
// say, and this is the reason this indexer is worth having.
//
// TPB and 1337x publish a name and nothing else, so parseSeasonEpisode has to
// recover the season from scene convention and is careful to the point of
// refusing anything ambiguous — a wrong season is a claim a name never made.
// EZTV answers with the fields, so there is nothing to recover.
//
// The fallback fires on a season of 0 alone, not on an episode of 0. An episode
// of 0 beside a stated season is EZTV's spelling of a SEASON PACK — measured:
// "Silo S02 1080p x265-ELiTE EZTV" arrives as season "2", episode "0" — and it
// is exactly what D49's TierPack is for. Re-reading that row's name would
// produce the same answer at best and lose the season at worst.
func eztvSeasonEpisode(name string, t eztvTorrent) (season, episode int) {
	if t.Season > 0 {
		return int(t.Season), int(t.Episode)
	}
	// Nothing stated. Fall back to the name, which is what every other indexer
	// has to do for every row.
	return parseSeasonEpisode(name)
}

// eztvIMDbID normalises an IMDb id to the digits EZTV's imdb_id parameter wants,
// returning "" for anything that is not one.
//
// TMDB reports "tt14688458" and this API wants "14688458". Both spellings are
// accepted so a caller need not know which side of the boundary it is on, and
// anything else — a title, a TMDB id, an empty string — is rejected rather than
// sent: a request with a malformed id is not an error at EZTV, it is a page of
// the whole site's newest uploads (see Answers).
func eztvIMDbID(raw string) string {
	id := strings.TrimSpace(raw)
	id = strings.TrimPrefix(strings.ToLower(id), "tt")
	if id == "" {
		return ""
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			return ""
		}
	}
	return id
}

// eztvBodySnippet reads the front of a failing response so the operator sees
// why, capped and flattened to one line for the reason ytsBodySnippet is: a
// host that is unwell answers with an HTML error page, and none of it belongs
// in an error string.
func eztvBodySnippet(r io.Reader) string {
	raw, err := io.ReadAll(io.LimitReader(r, 256))
	if err != nil || len(raw) == 0 {
		return "no body"
	}
	return strings.Join(strings.Fields(string(raw)), " ")
}

// Compile-time proof that EZTV declares both of its limitations.
var (
	_ Indexer      = (*EZTV)(nil)
	_ MediaCapable = (*EZTV)(nil)
	_ QueryCapable = (*EZTV)(nil)
)
