package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const ytsIndexerName = "yts"

// DefaultYTSBaseURL is the YTS API root, overridable via WithYTSBaseURL so tests
// can point the indexer at an httptest.Server.
//
// It is not yts.mx, which docs/tasks/T8-yts-indexer.md names: that domain is
// NXDOMAIN as of 2026-08-12 and resolves nowhere — not on 1.1.1.1, not on 8.8.8.8
// and not from the Pi. The surviving front doors yts.lt and yts.am both 301 to
// https://yts.gg/api/v2, which answers correctly but appends to every reply:
// "NOTICE: Base URL moving to https://movies-api.accel.li/api/v2/". This is that
// host — named by the API itself, and returning a `data` object we diffed against
// yts.gg's and found equal, without the notice. If it ever stops answering,
// https://yts.gg/api/v2 is the working alternative.
//
// Do not switch to yts.rs or yts.hn. They reply
// {"message":"Cannot read property 'moviesPerPage' of undefined"} — a different,
// fake implementation. docs/decisions.md D7 is about exactly these clones.
//
// Only the API host is open: https://yts.gg/movies/... serves a Cloudflare
// challenge, so scraping YTS would need minter. Querying the JSON API does not.
const DefaultYTSBaseURL = "https://movies-api.accel.li/api/v2"

// ytsStatusOK is the only `status` YTS's own API has been observed to return: it
// answers "ok" even to nonsense parameters (limit=999, sort_by=nonsense).
//
// The check that uses it is there for the clones. Their reply is valid JSON with
// no `status` and no `data`, which would otherwise decode cleanly into zero
// movies and be reported to the user as "nothing found".
const ytsStatusOK = "ok"

// ytsRequestTimeout bounds one search. YTS answers in well under a second (~0.5 s
// measured), and phase 2's whole-search deadline is 30 s shared across three
// indexers, so a YTS that has gone quiet must give up long before that budget.
const ytsRequestTimeout = 15 * time.Second

// ytsTrackers are the trackers stapled to every magnet we build.
//
// A bare "magnet:?xt=urn:btih:..." announces to nothing and leans entirely on
// DHT, which for a cold torrent can mean minutes before the first peer. Phase 2
// is done when magnets *work*, so the announce list is not optional.
//
// YTS's own .torrent files still ship the 2014 YIFY announce-list and most of it
// is gone: tracker.yify-torrents.com, tracker.publicbt.com,
// tracker.leechers-paradise.org and 9.rarbg.com are all NXDOMAIN, and
// tracker.istole.it, tracker.coppersurfer.tk and tracker.openbittorrent.com never
// answered. Each of these six replied to a BEP-15 connect handshake when probed
// on 2026-08-12. The first two are the survivors of YTS's own list, so YTS peers
// are genuinely announcing there; the rest are broad public trackers that widen
// the odds of finding one.
var ytsTrackers = []string{
	"udp://open.demonii.com:1337/announce",
	"udp://exodus.desync.com:6969/announce",
	"udp://tracker.opentrackr.org:1337/announce",
	"udp://open.stealth.si:80/announce",
	"udp://tracker.torrent.eu.org:451/announce",
	"udp://tracker.dler.org:6969/announce",
}

// YTS searches the YTS/YIFY catalogue. It is the cheap indexer: a JSON API with
// quality, size, seed count and info hash as fields, so there is no name to parse
// and no browser to drive. The catalogue is narrow — one curated encode per
// quality, movies only — which is why it is one of three sources and not the one.
type YTS struct {
	http    *http.Client
	baseURL string
}

// YTSOption customises a YTS indexer.
type YTSOption func(*YTS)

// WithYTSBaseURL points the indexer at another API root, so tests can substitute
// an httptest.Server. A trailing slash is trimmed so paths join predictably.
func WithYTSBaseURL(base string) YTSOption {
	return func(y *YTS) { y.baseURL = strings.TrimSuffix(base, "/") }
}

// NewYTS returns a YTS indexer fetching through httpClient. The HTTP client is
// injected so callers share one connection pool; a nil one gets a default with a
// timeout, since http.DefaultClient has none and would hang forever.
func NewYTS(httpClient *http.Client, opts ...YTSOption) *YTS {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: ytsRequestTimeout}
	}
	y := &YTS{http: httpClient, baseURL: DefaultYTSBaseURL}
	for _, opt := range opts {
		opt(y)
	}
	return y
}

// Name implements Indexer.
func (y *YTS) Name() string { return ytsIndexerName }

// Search implements Indexer. Every returned Release already carries a
// magnet — YTS gives the info hash in the search response, so there is nothing to
// resolve lazily.
//
// An empty slice with a nil error means "YTS does not have this film". That is a
// normal outcome for a catalogue this narrow, not a failure: phase 2 merges three
// sources precisely because none of them has everything.
func (y *YTS) Search(ctx context.Context, q Query) ([]Release, error) {
	query := strings.TrimSpace(q.Title)
	if query == "" {
		return nil, errors.New("yts search: empty title")
	}

	ctx, cancel := context.WithTimeout(ctx, ytsRequestTimeout)
	defer cancel()

	// The year is deliberately not part of query_term, even though 1337x's
	// searchQuery does append it. YTS matches query_term against the title as it
	// renders it ("Interstellar (2014)"), so a year that agrees is redundant and a
	// year that disagrees is fatal: "Interstellar 9999" comes back with zero
	// results rather than the film. Filtering on the movie's own year field below
	// cannot fail that way.
	params := url.Values{}
	params.Set("query_term", query)
	endpoint := y.baseURL + "/list_movies.json?" + params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("yts search %q: build request: %w", query, err)
	}
	// No User-Agent is set: Go's default is accepted as-is. The host does filter
	// some (Python-urllib gets a 403), so if searches start returning 403 this is
	// the line to add one to.
	req.Header.Set("Accept", "application/json")

	resp, err := y.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("yts search %q: %w", query, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("yts search %q: unexpected status %s: %s", query, resp.Status, ytsBodySnippet(resp.Body))
	}

	var body ytsResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("yts search %q: decode response: %w", query, err)
	}
	if body.Status != ytsStatusOK {
		message := body.StatusMessage
		if message == "" {
			message = "no status_message"
		}
		return nil, fmt.Errorf("yts search %q: api status %q: %s", query, body.Status, message)
	}

	return ytsReleases(body.Data.Movies, q.searchYear()), nil
}

// ytsResponse is the subset of list_movies.json this package reads. The field
// names come off a recorded response (testdata/yts-interstellar.json), not off
// the API's documentation — the seed count is `seeds`, not `seeders`, and the
// byte count is `size_bytes` beside a human-readable `size` string.
type ytsResponse struct {
	Status        string  `json:"status"`
	StatusMessage string  `json:"status_message"`
	Data          ytsData `json:"data"`
}

// ytsData is one page of results.
//
// `movie_count` is deliberately not read. It is the total YTS claims to have
// matched, and it disagrees with what arrives: query_term "Dune Part Two 1901"
// answers movie_count 1 with no `movies` key at all — recorded verbatim in
// testdata/yts-count-without-movies.json. The movies actually sent are the only
// thing worth trusting, so "did we find anything" is len(Movies), never the count.
type ytsData struct {
	Movies []ytsMovie `json:"movies"`
}

// ytsMovie is one film. YTS's model is a movie with torrents hanging off it, not
// a list of releases, which is why flattening is this package's job.
type ytsMovie struct {
	Title     string       `json:"title"`      // "Interstellar"
	TitleLong string       `json:"title_long"` // "Interstellar (2014)"
	Year      int          `json:"year"`
	Torrents  []ytsTorrent `json:"torrents"`
}

// ytsTorrent is one download of one movie.
//
// The payload's `is_repack`, `bit_depth` and `audio_channels` are JSON *strings*
// ("0", "8", "5.1"), not numbers. None of them is read here; if one ever is, do
// not assume the obvious Go type.
type ytsTorrent struct {
	Hash       string `json:"hash"`        // 40 uppercase hex
	Quality    string `json:"quality"`     // "720p", "1080p", "2160p" — and "3D"
	Type       string `json:"type"`        // "bluray", "web"
	VideoCodec string `json:"video_codec"` // "x264", "x265"
	Seeds      int    `json:"seeds"`
	SizeBytes  int64  `json:"size_bytes"`
}

// ytsReleases flattens movies into one Release per torrent, keeping only the
// movies matching year when one is given.
//
// Per torrent, not per movie: YTS lists the 720p, 1080p and 2160p cuts of a film
// as separate entries with their own hashes, sizes and seed counts, and they are
// genuinely different downloads.
func ytsReleases(movies []ytsMovie, year int) []Release {
	var out []Release
	for _, m := range movies {
		// Filtered on the movie's own year, so a search for "Dune" in 2021 keeps
		// Part One and drops Part Two. year <= 0 means the caller did not ask, and
		// keeps everything.
		if year > 0 && m.Year != year {
			continue
		}
		movieName := m.TitleLong
		if movieName == "" {
			movieName = m.Title
		}
		for _, t := range m.Torrents {
			hash := ytsInfoHash(t.Hash)
			if hash == "" {
				// No usable hash means no magnet, and YTS has no detail page to
				// resolve one from later. An empty Release.Magnet means "not
				// resolved yet" (see indexer.go) — for YTS that would be a lie
				// nobody could act on, so the torrent is dropped instead.
				continue
			}
			name := ytsReleaseTitle(movieName, t)
			out = append(out, Release{
				Title: name,
				// The movie's own year, unlike 1337x's toReleases which can only
				// echo back the year that was searched for. YTS states it, so a
				// search with no year still yields a real year on every Release.
				Year: m.Year,
				// Straight from the API. parseQuality reads resolutions out of
				// scene names; running it over the name we just built could only
				// lose information YTS already gave us — and it would silently
				// rewrite "3D", which YTS really does return, into QualityUnknown.
				Quality:   t.Quality,
				SizeBytes: t.SizeBytes,
				// `seeds` is clamped at 100 by the API: across a 50-movie sample
				// sorted by seeds, no torrent reported more, while `peers` on the
				// same torrents ranged freely. Ranking by seeders therefore sees
				// YTS's popular releases as a plateau rather than a ranking.
				Seeders: t.Seeds,
				Magnet:  ytsMagnet(hash, name),
				Indexer: ytsIndexerName,
			})
		}
	}
	return out
}

// ytsSourceNames spells the `type` values the way YTS's own site renders them.
// Only the two values seen across a 100-torrent sample are listed; anything else
// passes through verbatim rather than being guessed at.
var ytsSourceNames = map[string]string{"bluray": "BluRay", "web": "WEB"}

// ytsReleaseTitle names a torrent, because YTS does not: the API describes a
// *movie* and hangs torrents off it as attributes, so there is no release name in
// the payload to take.
//
// Quality, source and codec are all in the name because all three are needed to
// tell two rows apart — "Dune: Part Two (2024)" ships two 1080p WEB torrents
// differing only in codec, and with less than this they would render as the same
// line twice.
func ytsReleaseTitle(movie string, t ytsTorrent) string {
	var parts []string
	// Each piece is appended only if it is there, so a field YTS leaves blank
	// costs a bracket rather than producing an empty "[]" in the UI.
	for _, part := range []string{strings.TrimSpace(movie), ytsBracket(t.Quality), ytsBracket(ytsSourceName(t.Type)), ytsBracket(t.VideoCodec)} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, " ")
}

func ytsBracket(attr string) string {
	if attr = strings.TrimSpace(attr); attr == "" {
		return ""
	}
	return "[" + attr + "]"
}

// ytsSourceName renders a torrent's `type` for display, leaving anything not in
// ytsSourceNames exactly as the API sent it.
func ytsSourceName(typ string) string {
	if name, ok := ytsSourceNames[strings.ToLower(strings.TrimSpace(typ))]; ok {
		return name
	}
	return typ
}

// ytsInfoHash normalises a torrent hash, returning "" if it is not one.
//
// Every hash YTS has returned is 40 hex characters — the same shape InfoHash
// reads back out of a magnet, and the handle qBittorrent and the downloads table
// both key on. A magnet built from anything else would look perfectly valid and
// key wrongly, which is worse than one release fewer.
func ytsInfoHash(hash string) string {
	hash = strings.TrimSpace(hash)
	if len(hash) != 40 {
		return ""
	}
	for _, c := range hash {
		hex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !hex {
			return ""
		}
	}
	return strings.ToUpper(hash)
}

// ytsMagnet builds a magnet from an info hash, a display name and ytsTrackers.
//
// Hand-assembled rather than through url.Values, which would sort the keys and
// put xt last. Every magnet in the wild leads with xt, and while a client is free
// not to care, a magnet that looks unlike every other one is a debugging cost for
// no gain. Spaces in dn escape to "+", which is what 1337x's and YTS's own
// magnets carry.
func ytsMagnet(hash, name string) string {
	var b strings.Builder
	b.WriteString("magnet:?xt=urn:btih:")
	b.WriteString(hash)
	if name != "" {
		b.WriteString("&dn=")
		b.WriteString(url.QueryEscape(name))
	}
	for _, tracker := range ytsTrackers {
		b.WriteString("&tr=")
		b.WriteString(url.QueryEscape(tracker))
	}
	return b.String()
}

// ytsBodySnippet reads the front of a failing response so the operator sees why.
// It is capped and flattened to one line because a YTS host that is unwell
// answers with a full HTML error page — the 404 handler alone is several KB of
// markup — and none of that belongs in an error string.
func ytsBodySnippet(r io.Reader) string {
	raw, err := io.ReadAll(io.LimitReader(r, 256))
	if err != nil || len(raw) == 0 {
		return "no body"
	}
	return strings.Join(strings.Fields(string(raw)), " ")
}
