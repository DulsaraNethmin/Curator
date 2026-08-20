package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/DulsaraNethmin/curator/internal/jellyfin"
	"github.com/DulsaraNethmin/curator/internal/store"
	"github.com/DulsaraNethmin/curator/internal/tmdb"
)

// Browser is the TMDB catalogue as the browsing screens see it.
//
// It is a SECOND interface over the same *tmdb.Client rather than four more
// methods on Matcher. Matcher is phase 1's scan-time lookup, and widening it
// would force every phase 1 fake to implement four methods it never calls, for a
// feature it does not test. A nil Browser is a supported state, exactly like a
// nil Matcher and a nil Dispatcher.
type Browser interface {
	SearchMovies(ctx context.Context, query string, year int) ([]tmdb.Match, error)
	Movie(ctx context.Context, id int) (*tmdb.Details, error)
	Trending(ctx context.Context) ([]tmdb.Match, error)
	Popular(ctx context.Context) ([]tmdb.Match, error)
}

// WithBrowser attaches the catalogue and returns the server.
func (s *Server) WithBrowser(b Browser) *Server {
	s.browser = b
	return s
}

// MediaServer is the Open in Jellyfin link, and it is deliberately one method.
//
// An interface here rather than *jellyfin.Client for the reason Browser is one:
// the handler's behaviour when the lookup misses, when the key is revoked and
// when the server is off is the interesting part, and all three are awkward to
// produce from a real client. It is the fourth optional dependency with this
// shape — a nil one means no link at all, which is the rule the rest of the UI
// already follows for an unconfigured integration.
type MediaServer interface {
	// FindMovie answers the item for a TMDB id, or jellyfin.ErrNotFound.
	FindMovie(ctx context.Context, tmdbID, year int) (jellyfin.Item, error)
}

// WithJellyfin attaches the media server the movie screen can link into, and
// the base URL a BROWSER reaches it at — which is not the one curator uses when
// the two are in Docker together (docs/phase-8.md, jellyfin_public_url).
func (s *Server) WithJellyfin(m MediaServer, publicURL string) *Server {
	s.jellyfin = m
	s.jellyfinURL = publicURL
	return s
}

// RegisterBrowse mounts the TMDB-backed routes.
//
// Everything TMDB-backed lives under /api/tmdb/, and the prefix is the rule: if
// it is under /api/tmdb/, it goes dark without a key. /api/movies stays the
// library — what curator actually has — and the two can never be confused.
func (s *Server) RegisterBrowse(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/tmdb/discover", s.handleDiscover)
	mux.HandleFunc("GET /api/tmdb/search", s.handleTMDBSearch)
	mux.HandleFunc("GET /api/tmdb/movies/{id}", s.handleTMDBMovie)
}

// libraryStateBody is what curator already has for a film, or null.
//
// state is a store.Status* value or "downloading" — not a vocabulary invented
// for the UI. "downloading" comes from the downloads table rather than
// movies.status, because store.StatusDownloading is never written; see
// store.LibraryByTMDBID.
type libraryStateBody struct {
	MovieID     int64  `json:"movie_id"`
	State       string `json:"state"`
	LibraryPath string `json:"library_path,omitempty"`
}

// movieCard is one film as a poster grid shows it.
type movieCard struct {
	TMDBID       int               `json:"tmdb_id"`
	Title        string            `json:"title"`
	Year         int               `json:"year"`
	Overview     string            `json:"overview"`
	PosterPath   string            `json:"poster_path"`
	BackdropPath string            `json:"backdrop_path"`
	VoteAverage  float64           `json:"vote_average"`
	Library      *libraryStateBody `json:"library"`
}

// movieDetailBody is a card plus everything the movie screen shows.
type movieDetailBody struct {
	movieCard

	Tagline          string   `json:"tagline"`
	Runtime          int      `json:"runtime"`
	Genres           []string `json:"genres"`
	Status           string   `json:"status"`
	ReleaseDate      string   `json:"release_date"`
	OriginalLanguage string   `json:"original_language"`
	SpokenLanguages  []string `json:"spoken_languages"`
	Studios          []string `json:"studios"`
	Homepage         string   `json:"homepage"`
	IMDBID           string   `json:"imdb_id"`

	// JellyfinURL opens this film in Jellyfin, and it is ABSENT rather than
	// empty in the two states that mean "there is no link": no Jellyfin
	// configured, and a film curator does not have on disk. The UI draws
	// nothing for an absent one — not a disabled button and not a tooltip
	// explaining what you are missing, which is the rule the rest of the
	// screens already follow for an unconfigured integration.
	//
	// It is a deep link to the item when the lookup found one and a link to a
	// Jellyfin SEARCH for the title when it did not, and the UI is not told
	// which. Both land somewhere useful, the difference is not one a user can
	// act on, and a "we could not find it in Jellyfin" caption would be curator
	// explaining its own plumbing.
	JellyfinURL string `json:"jellyfin_url,omitempty"`
}

// discoverRow is one rail on the Discover screen.
//
// OK and Error are the indexers[] shape, deliberately. One rail failing is a
// success carrying the other, and a failing source that is invisible is minter's
// 200-carrying-a-failure wearing a third hat.
type discoverRow struct {
	ID      string      `json:"id"`
	Title   string      `json:"title"`
	OK      bool        `json:"ok"`
	Error   string      `json:"error,omitempty"`
	Results []movieCard `json:"results"`
}

func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request) {
	if s.browser == nil {
		s.failTMDB(w, errTMDBUnconfigured)
		return
	}

	rows := []discoverRow{
		{ID: "trending", Title: "Trending this week"},
		{ID: "popular", Title: "Popular"},
	}
	fetch := []func(context.Context) ([]tmdb.Match, error){
		s.browser.Trending,
		s.browser.Popular,
	}

	// Concurrently: two sequential ten-second timeouts would be twenty seconds of
	// home screen.
	var (
		wg      sync.WaitGroup
		results = make([][]tmdb.Match, len(rows))
		errs    = make([]error, len(rows))
	)
	for i := range rows {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = fetch[i](r.Context())
		}(i)
	}
	wg.Wait()

	library, err := s.store.LibraryByTMDBID(r.Context(), store.MediaTypeMovie)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	for i := range rows {
		if errs[i] != nil {
			// A failed rail is named, never hidden, and never fatal.
			//
			// The sentence is the same one failTMDB writes, because this is the
			// same failure — it is only the envelope that differs, and a reader
			// should not learn two vocabularies for one dependency. What was here
			// before was `errs[i].Error()` under a comment calling the chain
			// "exactly what the operator needs", which is true and is the reason
			// it now goes to the log instead of onto the home screen (D41).
			//
			// Logged here rather than through logCause: this envelope is 200, so
			// a gate on the status would drop the one 5xx-shaped failure in the
			// response. Warn rather than Error, because the page still draws and
			// /api/logs is a screen where red means something is broken.
			s.log.Warn("discover: a rail failed and is drawn empty",
				"row", rows[i].ID, "err", errs[i])
			rows[i].OK = false
			rows[i].Error = tmdbSentence(errs[i])
			rows[i].Results = []movieCard{}
			continue
		}
		rows[i].OK = true
		rows[i].Results = toCards(results[i], library)
	}

	s.respond(w, http.StatusOK, map[string]any{"rows": rows})
}

func (s *Server) handleTMDBSearch(w http.ResponseWriter, r *http.Request) {
	if s.browser == nil {
		s.failTMDB(w, errTMDBUnconfigured)
		return
	}

	query := strings.TrimSpace(r.URL.Query().Get("query"))
	if query == "" {
		s.fail(w, http.StatusBadRequest, errors.New("query is required"))
		return
	}
	year, err := parseYear(r.URL.Query().Get("year"))
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}

	matches, err := s.browser.SearchMovies(r.Context(), query, year)
	if err != nil {
		s.failTMDB(w, err)
		return
	}
	library, err := s.store.LibraryByTMDBID(r.Context(), store.MediaTypeMovie)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	s.respond(w, http.StatusOK, map[string]any{
		"query":   query,
		"year":    year,
		"results": toCards(matches, library),
	})
}

func (s *Server) handleTMDBMovie(w http.ResponseWriter, r *http.Request) {
	if s.browser == nil {
		s.failTMDB(w, errTMDBUnconfigured)
		return
	}

	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		s.fail(w, http.StatusBadRequest, errors.New("id must be a tmdb id"))
		return
	}

	details, err := s.browser.Movie(r.Context(), id)
	if err != nil {
		s.failTMDB(w, err)
		return
	}
	library, err := s.store.LibraryByTMDBID(r.Context(), store.MediaTypeMovie)
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	body := movieDetailBody{
		movieCard:        toCard(details.Match, library),
		Tagline:          details.Tagline,
		Runtime:          details.Runtime,
		Genres:           details.Genres,
		Status:           details.Status,
		ReleaseDate:      details.ReleaseDate,
		OriginalLanguage: details.OriginalLanguage,
		SpokenLanguages:  details.SpokenLanguages,
		Studios:          details.Studios,
		Homepage:         details.Homepage,
		IMDBID:           details.IMDBID,
	}
	// [] and never null: the UI iterates all three.
	if body.Genres == nil {
		body.Genres = []string{}
	}
	if body.Studios == nil {
		body.Studios = []string{}
	}
	if body.SpokenLanguages == nil {
		body.SpokenLanguages = []string{}
	}

	body.JellyfinURL = s.jellyfinLink(r.Context(), body)

	s.respond(w, http.StatusOK, body)
}

func toCards(matches []tmdb.Match, library map[int64]store.LibraryState) []movieCard {
	cards := make([]movieCard, 0, len(matches)) // [] and never null
	for _, m := range matches {
		cards = append(cards, toCard(m, library))
	}
	return cards
}

func toCard(m tmdb.Match, library map[int64]store.LibraryState) movieCard {
	card := movieCard{
		TMDBID:       m.TMDBID,
		Title:        m.Title,
		Year:         m.Year,
		Overview:     m.Overview,
		PosterPath:   m.PosterPath,
		BackdropPath: m.BackdropPath,
		VoteAverage:  m.VoteAverage,
	}

	state, ok := library[int64(m.TMDBID)]
	if !ok {
		return card // curator has never heard of this film; library stays null
	}

	body := &libraryStateBody{MovieID: state.MovieID, State: state.Status}
	if state.Downloading {
		// The download is the truth here, not movies.status, which never says
		// 'downloading' — see store.LibraryByTMDBID.
		body.State = store.StatusDownloading
	}
	if state.LibraryPath != nil {
		body.LibraryPath = *state.LibraryPath
	}
	card.Library = body
	return card
}

// jellyfinLink is the Open in Jellyfin URL for one film, or "" for no link.
//
// **It can never fail the page.** Every outcome except "found it" produces the
// search link, which needs no network at all and cannot 404 — a revoked key, a
// Jellyfin that is switched off, a film it has not indexed yet, and a film it
// has never matched to TMDB all land on the same fallback. That is why this
// returns a string and not an error: there is no failure here for a caller to
// handle, only a better link and a worse one.
//
// **Only for a film curator has on disk.** Linking a film nobody owns into a
// media server that certainly does not have it is a link to a search for
// something that is not there. store.StatusImported is the test rather than
// library != nil, because a wanted or downloading film is in the database and
// not on the disk.
func (s *Server) jellyfinLink(ctx context.Context, body movieDetailBody) string {
	tmdbID := int64(body.TMDBID)
	imported := body.Library != nil && body.Library.State == store.StatusImported
	return s.jellyfinLinkFor(ctx, &tmdbID, body.Year, body.Title, imported)
}

// jellyfinLinkFor is jellyfinLink's body, reached from the catalogue page above
// and from a library row that has no catalogue entry at all ([D35]).
//
// **A nil tmdbID is not a failure to report.** It is a row TMDB never matched
// ([D6]), and it is D32's miss arriving one step earlier than a lookup that
// comes back empty — so it takes the same search link, without a lookup it has
// no id to make. That is the whole reason this split exists: the fallback was
// already written for a Jellyfin that has not matched the film, and a curator
// that has not matched it is the same situation seen from the other end.
//
// [D35]: ../../docs/decisions.md
// [D6]: ../../docs/decisions.md
func (s *Server) jellyfinLinkFor(ctx context.Context, tmdbID *int64, year int, title string, imported bool) string {
	if s.jellyfin == nil || strings.TrimSpace(s.jellyfinURL) == "" {
		return ""
	}
	if !imported {
		return ""
	}
	if tmdbID == nil {
		return jellyfin.WebSearchURL(s.jellyfinURL, title)
	}

	// The lookup carries its own deadline inside internal/jellyfin, because
	// this one is in front of a person waiting for a page rather than behind a
	// poller. Measured against the real 10.10.7 over a LAN: 5.5 ms.
	item, err := s.jellyfin.FindMovie(ctx, int(*tmdbID), year)
	if err == nil {
		return jellyfin.WebItemURL(s.jellyfinURL, item)
	}

	// A miss is ordinary — Jellyfin scans on its own schedule, so a film
	// imported a minute ago is legitimately not there yet — and it is not worth
	// a line. Anything else is the operator's business: a revoked key and a
	// Jellyfin that is off both silently downgrade the link, and "why is Open
	// in Jellyfin always a search" is otherwise a question with no evidence.
	if !errors.Is(err, jellyfin.ErrNotFound) {
		s.log.Warn("jellyfin: could not look this film up, so the link is a search instead",
			"tmdb_id", *tmdbID, "err", err)
	}
	return jellyfin.WebSearchURL(s.jellyfinURL, title)
}

// errTMDBUnconfigured is the no-key state, phrased the way ErrUnconfigured is
// for downloads: it names the variable, because that is the whole remedy.
var errTMDBUnconfigured = errors.New("tmdb is not configured: set TMDB_API_KEY")

// failTMDB maps a catalogue failure onto a status that says whose problem it is.
func (s *Server) failTMDB(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errTMDBUnconfigured):
		s.fail(w, http.StatusServiceUnavailable, err)
	case errors.Is(err, tmdb.ErrNotFound):
		// The id came from a URL a human can type.
		s.fail(w, http.StatusNotFound, err)
	default:
		// 502 for both, and NOT 503. In this codebase 503 means "you did not set
		// the variable", which would be a lie when it is set and simply wrong —
		// and it would send someone to check a variable that is already there.
		s.failCause(w, http.StatusBadGateway, tmdbSentence(err), err)
	}
}

// tmdbSentence is the one place a TMDB failure becomes words.
//
// One function rather than two branches because the catalogue answers this
// failure at two different envelopes — 502 for a whole request, and a named rail
// inside an otherwise fine 200 on the home screen — and a reader should not have
// to learn two vocabularies for one dependency being unwell.
//
// The distinction it carries is the one the status cannot: a rejected key is not
// an unset one, and 502 says neither. The chain that says `tmdb <what>: ` and
// quotes TMDB's own status_message is the operator's half, and it goes to the log.
func tmdbSentence(err error) string {
	if errors.Is(err, tmdb.ErrUnauthorized) {
		return "TMDB rejected curator's API key, so the catalogue cannot be read — the key is set, it is being refused"
	}
	// Everything else TMDB can do: a 500, a timeout, a body that is not the JSON
	// the client expects. One sentence covers them because there is one remedy,
	// and none of it is the reader's to fix.
	return "TMDB did not answer in a way curator understands, so the catalogue cannot be read just now — this is TMDB's end, not yours or curator's"
}
