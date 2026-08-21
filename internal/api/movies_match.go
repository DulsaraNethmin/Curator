package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/DulsaraNethmin/curator/internal/store"
	"github.com/DulsaraNethmin/curator/internal/tmdb"
)

// RegisterMovieMatch mounts the two routes that write a tmdb_id a human chose.
//
// It is registered separately from Register for the reason RegisterMovieDelete
// is: phase 1's route set keeps its shape, and a deployment could omit it.
//
// **POST establishes a match and PUT replaces one, and they are two methods on
// one path rather than two paths.** The resource is the same in both cases — the
// row's match — so the distinction is what is being done to it, which is what a
// method is for. Go 1.22 routing dispatches on the method, so the pair costs one
// line. The alternative considered was a flag in the body (`{"replace":true}`),
// and it loses on the property that matters here: a client that forgets the flag
// silently gets the safe refusal, whereas a client that sends the wrong method
// gets a 409 naming the other one.
func (s *Server) RegisterMovieMatch(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/movies/{id}/match", s.handleMatchMovie)
	mux.HandleFunc("PUT /api/movies/{id}/match", s.handleCorrectMatch)
}

// matchRequest is the whole body. Only the id: everything else about the film is
// read from TMDB rather than accepted from the caller (see handleMatchMovie).
type matchRequest struct {
	TMDBID int64 `json:"tmdb_id"`
}

// handleMatchMovie points a library row that has no match at the film a human
// picked. handleCorrectMatch is the same request against a row that has one.
func (s *Server) handleMatchMovie(w http.ResponseWriter, r *http.Request) {
	s.applyMatch(w, r, matchWrite{
		refuse: func(movie store.Movie) error {
			if movie.TMDBID != nil {
				return store.ErrAlreadyMatched
			}
			return nil
		},
		write:  s.store.MatchMovie,
		logged: "matched by hand",
	})
}

// handleCorrectMatch repoints a row that is already matched at a different film.
//
// It is T67's deliberate gap closed. That task refused to correct a match and said
// why — *"it needs its own way in, because a matched card routes to `/movie/` and
// never reaches this page"* — so building the write then would have shipped a code
// path with no caller. The way in exists now: `/movie/` links a film it holds to
// that film's library row, and the picker on that page opens for a matched row.
//
// **The store call is CorrectMatch and not a clear followed by MatchMovie**, and
// the reason is a race rather than a preference: a row with a NULL tmdb_id is
// exactly what MoviesMissingMetadata selects, so a scan between the two requests
// would re-match it from the folder name that produced the wrong match. See
// store.CorrectMatch, which carries the measurement.
func (s *Server) handleCorrectMatch(w http.ResponseWriter, r *http.Request) {
	s.applyMatch(w, r, matchWrite{
		refuse: func(movie store.Movie) error {
			if movie.TMDBID == nil {
				return store.ErrNotMatched
			}
			return nil
		},
		write:  s.store.CorrectMatch,
		logged: "match corrected by hand",
	})
}

// matchWrite is what separates the two handlers, which is three things: the
// refusal that decides whether this row is theirs to write, the store call, and
// the word in the log. Everything else — the key check, the body, the TMDB
// re-read, the response — is identical, and sharing it is what keeps a match and a
// correction indistinguishable in the row afterwards.
type matchWrite struct {
	refuse func(store.Movie) error
	write  func(context.Context, int64, store.TMDBMatch) (store.Movie, error)
	logged string
}

// applyMatch is both routes: read the row, refuse it if this method is the wrong
// one for it, re-read the film from TMDB, write, and answer the row.
//
// **These live under /api/movies/ although they need the TMDB key**, which is
// worth stating because browse.go's prefix rule says everything TMDB-backed lives
// under /api/tmdb/. The subject of the request is a library row — it is addressed
// by curator's own movies.id, it writes to the library, and it answers a library
// row. TMDB is an input to it, not what it is about. What the prefix rule actually
// guarantees is that a keyless install gets a 503 naming the variable instead of a
// confusing failure, and these routes keep that by calling failTMDB with the same
// error the /api/tmdb/ handlers use.
//
// The catalogue is re-read rather than trusted. The body carries an id and nothing
// else, and Browser.Movie turns it into the overview and poster the scan would
// have written — so a row matched by hand, a row corrected by hand and a row
// matched by the scanner are indistinguishable afterwards, and an id for a film
// TMDB does not have is a 404 instead of a row pointing at nothing.
func (s *Server) applyMatch(w http.ResponseWriter, r *http.Request, kind matchWrite) {
	if s.browser == nil {
		s.failTMDB(w, errTMDBUnconfigured)
		return
	}

	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.fail(w, http.StatusBadRequest, fmt.Errorf("id %q: not a number", r.PathValue("id")))
		return
	}

	var body matchRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		s.fail(w, http.StatusBadRequest, fmt.Errorf("body: %w", err))
		return
	}
	if body.TMDBID <= 0 {
		// Zero is what a missing key decodes to, so this covers both the absent
		// field and a nonsense one. D6 keeps "unmatched" and "matched to 0"
		// distinguishable in the database and this is the same distinction
		// arriving over HTTP.
		s.fail(w, http.StatusBadRequest, errors.New("tmdb_id is required and must be a positive number"))
		return
	}

	// The row is read first so a request naming a row that does not exist, or one
	// this method is not the right one for, does not spend a TMDB request finding
	// that out. The store re-checks the same thing under the write lock; this is
	// the cheap copy, not the guard.
	movie, err := s.store.GetMovie(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.fail(w, http.StatusNotFound, fmt.Errorf("no movie with id %d", id))
			return
		}
		s.fail(w, http.StatusInternalServerError, err)
		return
	}
	// **Films only, and refusing is the whole point of this check.**
	//
	// The lookup below is s.browser.Movie — TMDB's /movie/{id} — and the write
	// underneath it goes through store's tmdbIDWrite, a CASE over the ROW's own
	// media_type. Those two are each correct and together they are a trap: hand
	// this a television row and it resolves a FILM, then stores that film's id
	// in tmdb_tv_id. Silent, permanent, and it would make every later lookup for
	// that show ask Jellyfin and TMDB about a film nobody mentioned.
	//
	// Refusing rather than resolving the right endpoint is deliberate for one
	// release. Matching a show by hand needs its own route reading
	// browser.Show — /api/shows/{id}/match — and that is a feature; this is the
	// guard that stops the feature's absence corrupting a row. Nothing regresses:
	// no screen offers a hand match for a show, so the only way to reach this is
	// a hand-written request.
	// It sits ABOVE kind.refuse, so the answer does not depend on whether the
	// show happens to be matched yet. "This route is for films" is a fact about
	// the route; "there is nothing to correct" is a fact about the row, and a
	// television row should get the first one either way rather than a sentence
	// about a film's match state that happens to arrive first.
	if movie.MediaType == store.MediaTypeTV {
		s.fail(w, http.StatusBadRequest, fmt.Errorf(
			"movie %d is a television show, and this route matches films: "+
				"it would look up a film and store its id as the show's", id))
		return
	}

	if err := kind.refuse(movie); err != nil {
		s.failMatch(w, err)
		return
	}

	details, err := s.browser.Movie(r.Context(), int(body.TMDBID))
	if err != nil {
		s.failTMDB(w, err)
		return
	}

	// Title stays nil on purpose, exactly as the scan's match does: TMDB's
	// canonical title would undo the " - " substitution that identifies the
	// folder (D9), and library_path is the row's identity anyway.
	//
	// The year IS written, and into `tmdb_year` rather than `year` — T67 wrote it
	// onto `year` and watched the next scan take it back. `year` is the folder's
	// and stays the folder's; this is the number Jellyfin is asked with (D37).
	match := store.TMDBMatch{TMDBID: body.TMDBID}
	if details.Overview != "" {
		match.Overview = &details.Overview
	}
	if details.PosterPath != "" {
		match.PosterPath = &details.PosterPath
	}
	if details.Year > 0 {
		// A film with no release date leaves this NULL rather than storing 0,
		// which is the same thing FindMovie does with it: skip the narrowing
		// rather than narrow to a year nothing has.
		year := details.Year
		match.Year = &year
	}

	matched, err := kind.write(r.Context(), id, match)
	if err != nil {
		s.failMatch(w, err)
		return
	}

	s.log.Info(kind.logged, "movie_id", id, "tmdb_id", body.TMDBID, "title", matched.Title)

	// The same body GET /api/movies/{id} answers, so the page can swap the row it
	// is holding without a reload — and jellyfin_url really does change, because
	// jellyfinLinkFor stops taking the nil-tmdbID search branch and starts looking
	// the film up for a deep link.
	//
	// The media type comes off the ROW rather than being stated as a film,
	// because that is the fact that decides which query Jellyfin is asked —
	// and this route is reached with a movies.id, which is one table.
	out := movieBody{Movie: matched}
	out.JellyfinURL = s.jellyfinLinkFor(r.Context(), matched.MediaType,
		matched.TMDBID, matched.MatchYear(), matched.Title, matched.Status == store.StatusImported)
	s.respond(w, http.StatusOK, out)
}

// failMatch maps the store's three refusals onto 409, and everything else onto 500.
//
// All three are 409 rather than 400 for the reason the delete handler's
// ErrWrongCategory is: the request is well-formed and the refusal is deliberate.
// They stay three distinct messages because the remedies differ — two of them name
// the other method, and the third says the film is already in the library
// somewhere else.
//
// **The ErrAlreadyMatched sentence changed with T69 and the old one is worth not
// restoring.** It read *"…so there is nothing to correct here"*, which was true
// while a wrong match had no remedy and became a falsehood the moment one did. A
// message that names the impossible is worse than a terse one, because somebody
// reads it and stops looking.
func (s *Server) failMatch(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		s.fail(w, http.StatusNotFound, err)
	case errors.Is(err, store.ErrAlreadyMatched):
		s.fail(w, http.StatusConflict,
			errors.New("this film is already matched to a TMDB film; correcting that match is a PUT, not a POST"))
	case errors.Is(err, store.ErrNotMatched):
		s.fail(w, http.StatusConflict,
			errors.New("this film has no TMDB match yet, so there is nothing to correct; matching it is a POST, not a PUT"))
	case errors.Is(err, store.ErrTMDBIDTaken):
		s.fail(w, http.StatusConflict,
			errors.New("curator already has that film in the library under another folder"))
	case errors.Is(err, tmdb.ErrNotFound):
		s.fail(w, http.StatusNotFound, err)
	default:
		s.fail(w, http.StatusInternalServerError, err)
	}
}
