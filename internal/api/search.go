package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/DulsaraNethmin/curator/internal/indexer"
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
type searchResponse struct {
	Title    string        `json:"title"`
	Year     int           `json:"year"`
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

	// Films, spelled out rather than left to the zero value: this handler is the
	// film one, and a television search reaches the indexers through a query of
	// its own rather than by this route learning a second media type.
	result, err := s.searcher.Search(r.Context(), indexer.Query{
		Title: title,
		Year:  year,
		Media: indexer.MediaMovie,
	})
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

	s.log.Info("search complete", "title", title, "year", year, "releases", len(out.Releases))
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
