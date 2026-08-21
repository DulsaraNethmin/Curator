package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/DulsaraNethmin/curator/internal/indexer"
	"github.com/DulsaraNethmin/curator/internal/store"
)

// Searcher is the release search the handlers need. Declared here rather than
// taken as a concrete type, like Store, Scanner and Matcher, so the handlers can
// be exercised against fakes instead of three live indexers and a browser.
type Searcher interface {
	Search(ctx context.Context, q indexer.Query) (indexer.SearchResult, error)
	ResolveMagnet(ctx context.Context, id string) (string, error)
}

// WithSearch attaches the release search and returns the server, so wiring reads
// as one expression in main.
func (s *Server) WithSearch(searcher Searcher) *Server {
	s.searcher = searcher
	return s
}

// RegisterSearch mounts phase 2's routes. Separate from Register so that a server
// built without a searcher — every phase 1 test — cannot accidentally serve a
// search that would nil-panic.
func (s *Server) RegisterSearch(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/search", s.handleSearch)
	mux.HandleFunc("GET /api/releases/{id}/magnet", s.handleResolveMagnet)
}

// searchResponse is what GET /api/search returns.
//
// Media and Season echo what was actually searched for. They are not
// decoration: a season selector that fires a request per change has to be able
// to tell the answer to "season 2" from the answer to "season 3" still in
// flight, and the response is the only place that fact exists.
type searchResponse struct {
	Title  string `json:"title"`
	Year   int    `json:"year"`
	Media  string `json:"media"`
	Season int    `json:"season,omitempty"`

	// Episode echoes the episode asked for, for the same reason Season does:
	// walking a season one episode at a time fires a request per change, and
	// this is the only place the answer says which episode it is the answer to.
	Episode  int           `json:"episode,omitempty"`
	Releases []releaseBody `json:"releases"`
	Indexers []indexerBody `json:"indexers"`
}

// releaseBody is one candidate download.
//
// Magnet is a pointer so an unresolved release serialises as null. An empty string
// would read as "there is no magnet"; null means "not resolved yet", which for a
// 1337x release is the normal state until someone picks it.
type releaseBody struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Year      int      `json:"year"`
	Quality   string   `json:"quality"`
	SizeBytes int64    `json:"size_bytes"`
	Seeders   int      `json:"seeders"`
	Indexers  []string `json:"indexers"`
	Magnet    *string  `json:"magnet"`

	// Season and Episode are what the release NAME says, read by the indexers
	// rather than taken from the query — a season pack has a season and no
	// episode, a single episode has both, and a film has neither. `omitempty`
	// because a film search is exactly as it was before television existed,
	// and because 0 here means "the name does not say" rather than season 0.
	Season  int `json:"season,omitempty"`
	Episode int `json:"episode,omitempty"`

	// Match is how directly this release answers the search: "exact", "pack" for
	// a season pack offered against a single-episode query, or "unstated" for a
	// name that claims no season at all. Empty when the search named no season,
	// because then there is nothing for a release to be a near-miss of.
	//
	// Sent rather than left for the screen to work out. The rule has three cases
	// and a fourth that is dropped before it ever gets here, and a client
	// re-deriving "a pack is one whose episode is 0" is a second copy that
	// agrees today and drifts the first time the rule gains a case.
	// indexer.SeasonTier is the one definition; this is it, spelled for JSON.
	Match string `json:"match,omitempty"`
}

// matchName spells indexer's tier for the API. The tiers are an ordering
// internal to ranking; these are a vocabulary a screen groups on, and keeping
// them separate means the order can gain a tier without changing the wire.
func matchName(tier int) string {
	switch tier {
	case indexer.TierExact:
		return "exact"
	case indexer.TierPack:
		return "pack"
	case indexer.TierUnstated:
		return "unstated"
	default:
		// TierWrong is filtered out before the API sees it, so this is
		// unreachable rather than a case with a meaning. Empty, not a guess.
		return ""
	}
}

// indexerBody is one indexer's report.
//
// This block is not decoration. "A failing indexer is omitted, never fatal" must
// not become "a failing indexer is invisible" — that is minter's
// 200-carrying-a-failure bug wearing a different hat. A search where 1337x is down
// still returns 200, and says which source let it down.
type indexerBody struct {
	Name  string `json:"name"`
	OK    bool   `json:"ok"`
	Count int    `json:"count"`
	Error string `json:"error,omitempty"`

	// Unconfigured separates the indexer that never started from the one that
	// tried and failed, because they are two different instructions: wait, or
	// run one line. It is `omitempty` because false is the ordinary case and a
	// key on every outcome would read as a state every indexer has.
	Unconfigured bool `json:"unconfigured,omitempty"`

	// NotApplicable separates the indexer that was never asked from both of
	// them: this media type is not one it has, so there is no instruction at
	// all. Without it, YTS would answer a television search with the empty
	// result its own documentation calls "does not have this film", and the
	// screen would show ok:true, count:0 — a lie in the format that reads as
	// "nobody uploaded it". `omitempty` for the same reason as above.
	NotApplicable bool `json:"not_applicable,omitempty"`
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	title := strings.TrimSpace(r.URL.Query().Get("title"))
	if title == "" {
		s.fail(w, http.StatusBadRequest, errors.New("title is required"))
		return
	}

	year, err := parseYear(r.URL.Query().Get("year"))
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	mediaType, ok := s.media(w, r)
	if !ok {
		return
	}
	season, err := parseSeason(r.URL.Query().Get("season"), mediaType)
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	episode, err := parseEpisode(r.URL.Query().Get("episode"), season, mediaType)
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	imdbID, err := parseIMDbID(r.URL.Query().Get("imdb_id"))
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}

	// The media type is spelled out rather than left to the zero value, even
	// though the zero value means the same thing: it is what decides TPB's
	// cat= and the keyword string 1337x is handed, and neither is recoverable
	// from a title however it is spelled (internal/indexer, Query).
	query := indexer.Query{
		Title:   title,
		Year:    year,
		Media:   mediaType,
		Season:  season,
		Episode: episode,
		IMDBID:  imdbID,
	}
	result, err := s.searcher.Search(r.Context(), query)
	if err != nil {
		// Only the caller's own context failing gets here: an indexer failing —
		// even all of them — comes back as a reported outcome, not an error.
		s.fail(w, http.StatusInternalServerError, fmt.Errorf("search %q: %w", title, err))
		return
	}

	releases := indexer.FilterFound(result.Releases, splitQuality(r.URL.Query().Get("quality")))

	out := searchResponse{
		Title:    title,
		Year:     year,
		Media:    mediaType,
		Season:   season,
		Episode:  episode,
		Releases: make([]releaseBody, 0, len(releases)),
		Indexers: make([]indexerBody, 0, len(result.Outcomes)),
	}
	for _, f := range releases {
		body := releaseBody{
			ID:        f.ID,
			Title:     f.Title,
			Year:      f.Year,
			Quality:   f.Quality,
			SizeBytes: f.SizeBytes,
			Seeders:   f.Seeders,
			Indexers:  f.Indexers,
			Magnet:    nil,
			Season:    f.Season,
			Episode:   f.Episode,
		}
		// Only when a season was named. Without one every release is TierExact
		// by definition, and stamping "exact" on a film's releases would be
		// noise on every search that has nothing to do with television.
		if season > 0 {
			body.Match = matchName(indexer.SeasonTier(f.Release, query))
		}
		if f.Magnet != "" {
			magnet := f.Magnet
			body.Magnet = &magnet
		}
		out.Releases = append(out.Releases, body)
	}
	for _, o := range result.Outcomes {
		out.Indexers = append(out.Indexers, indexerBody{
			Name: o.Name, OK: o.OK, Count: o.Count, Error: o.Error,
			Unconfigured:  o.Unconfigured,
			NotApplicable: o.NotApplicable,
		})
	}

	s.log.Info("search complete", "title", title, "year", year,
		"media", mediaType, "season", season, "episode", episode, "releases", len(out.Releases))
	s.respond(w, http.StatusOK, out)
}

// magnetResponse is what resolving a release id returns.
type magnetResponse struct {
	Magnet   string `json:"magnet"`
	InfoHash string `json:"info_hash"`
}

func (s *Server) handleResolveMagnet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	magnet, err := s.searcher.ResolveMagnet(r.Context(), id)
	if err != nil {
		if errors.Is(err, indexer.ErrReleaseExpired) {
			// 410 rather than 404: the id was almost certainly real, and the caller
			// needs to know that searching again will produce a working one. A silent
			// re-search on our side would resolve a different release than the one
			// they picked.
			s.fail(w, http.StatusGone, fmt.Errorf("release %s is no longer available: search again", id))
			return
		}
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	s.respond(w, http.StatusOK, magnetResponse{Magnet: magnet, InfoHash: indexer.InfoHash(magnet)})
}

// parseYear reads the optional year filter. Absent or empty is no filter, not an
// error — searching without a year is a normal thing to do.
func parseYear(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	year, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("year %q: not a number", raw)
	}
	if year < 0 {
		return 0, fmt.Errorf("year %d: not a year", year)
	}
	return year, nil
}

// parseSeason reads the optional season. Absent or empty is no constraint —
// searching a show without naming a season is how you find a complete-series
// pack — and so is 0, which internal/indexer already reads as "no season".
//
// **A season on a film request is a 400 rather than an ignored parameter.** It
// is not a value a person types; it is a UI sending one media type's control
// with the other media type's request, and answering a film search to it would
// hide that from every screen. Season 0 is not rejected: it is a real season
// number for specials, and library.SeasonFolder(0) is "Season 00" for exactly
// that reason — it simply constrains nothing at the indexers.
func parseSeason(raw, mediaType string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	season, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("season %q: not a number", raw)
	}
	return season, validateSeason(season, mediaType)
}

// validateSeason is the half of parseSeason that does not care where the number
// came from — the query string here, a JSON number on POST /api/downloads.
func validateSeason(season int, mediaType string) error {
	if season < 0 {
		return fmt.Errorf("season %d: not a season", season)
	}
	if season > 0 && mediaType != store.MediaTypeTV {
		return fmt.Errorf("season %d: only television has seasons", season)
	}
	return nil
}

// parseEpisode reads the optional episode, which is parseSeason's shape with one
// extra rule: an episode needs the season it belongs to.
//
// **An episode without a season is a 400 rather than a search for "episode 5 of
// any season".** No release names itself that way — the convention is S02E05 and
// a bare E05 matches nothing — so honouring it would mean a query that reliably
// finds nothing while reporting ok:true, count:0, which is the exact failure
// mode D20 and NormaliseQuery already exist to prevent. indexer.Query documents
// the same rule from its side and simply ignores the field; the refusal lives
// here because this is the edge where a caller can still be told.
//
// Episode 0 is accepted for the same reason season 0 is: it constrains nothing.
func parseEpisode(raw string, season int, mediaType string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	episode, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("episode %q: not a number", raw)
	}
	if episode < 0 {
		return 0, fmt.Errorf("episode %d: not an episode", episode)
	}
	if episode > 0 && mediaType != store.MediaTypeTV {
		return 0, fmt.Errorf("episode %d: only television has episodes", episode)
	}
	if episode > 0 && season == 0 {
		return 0, fmt.Errorf("episode %d: name a season too — an episode number means nothing without one", episode)
	}
	return episode, nil
}

// imdbIDPattern is `tt` followed by digits, which is the only shape TMDB emits.
// Seven digits is the historical length and IMDb has been issuing eight for
// years, so the count is left open rather than pinned to a number that will
// move again.
var imdbIDPattern = regexp.MustCompile(`^tt\d+$`)

// parseIMDbID reads the optional IMDb id. Absent or empty is not an error — it
// is the state every search made before this parameter existed, and every
// search from a screen that has no id to send.
//
// **A malformed one IS an error, and that is the whole reason this function
// exists rather than the raw string being passed through.** The one indexer
// that reads the id would simply decline a value it cannot use, which is
// indistinguishable on the screen from EZTV being switched off — so a client
// sending a TMDB id, or a title, in this parameter would silently get a search
// without EZTV in it and no way to find out. Refusing here is the edge where
// the caller can still be told.
//
// Accepted in TMDB's spelling, prefix and all, because that is what the show
// screen has in hand. Stripping it is EZTV's business (internal/indexer,
// eztvIMDbID) and happens at that indexer's boundary.
func parseIMDbID(raw string) (string, error) {
	id := strings.TrimSpace(raw)
	if id == "" {
		return "", nil
	}
	if !imdbIDPattern.MatchString(id) {
		return "", fmt.Errorf("imdb_id %q: an IMDb id is tt followed by digits", id)
	}
	return id, nil
}

// splitQuality parses ?quality=1080p,2160p. The spellings themselves are
// FilterQuality's business; this only splits and discards blanks, so a trailing
// comma is not read as a request for releases of quality "".
func splitQuality(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	for _, q := range strings.Split(raw, ",") {
		if q = strings.TrimSpace(q); q != "" {
			out = append(out, q)
		}
	}
	return out
}
