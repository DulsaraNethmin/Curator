package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"

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

	library, err := s.store.LibraryByTMDBID(r.Context())
	if err != nil {
		s.fail(w, http.StatusInternalServerError, err)
		return
	}

	for i := range rows {
		if errs[i] != nil {
			// A failed rail is named, never hidden, and never fatal — including a
			// rejected key, whose message is exactly what the operator needs.
			rows[i].OK = false
			rows[i].Error = errs[i].Error()
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
	library, err := s.store.LibraryByTMDBID(r.Context())
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
	library, err := s.store.LibraryByTMDBID(r.Context())
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
	case errors.Is(err, tmdb.ErrUnauthorized):
		// 502, NOT 503. In this codebase 503 means "you did not set the
		// variable", which would be a lie when it is set and simply wrong — and
		// it would send someone to check a variable that is already there.
		s.fail(w, http.StatusBadGateway, err)
	default:
		s.fail(w, http.StatusBadGateway, err)
	}
}
