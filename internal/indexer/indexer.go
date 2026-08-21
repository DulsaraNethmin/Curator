// Package indexer searches public torrent sources behind one interface.
//
// It was absorbed from the `cfprobe` prototype, which proved the 1337x path end
// to end: search through minter, parse the result table, resolve magnets lazily,
// filter by quality. The behaviour is a straight port so that any regression is
// attributable to a later change rather than to the move.
//
// Nothing wires this up in phase 1 — that is phase 2. It lives here so proven
// code is not lost or rewritten from memory later.
package indexer

import "context"

// The two media types a Query can ask for. They are the same two strings as
// store.MediaTypeMovie and store.MediaTypeTV, and they are spelled out again
// here rather than imported: internal/store brings the SQLite driver with it,
// and a package that searches torrent sites has no business depending on the
// database. internal/api holds a test that the two spellings still agree, which
// is the cheap half of the import without the dependency.
const (
	MediaMovie = "movie"
	MediaTV    = "tv"
)

// Query is one search: what to look for, and what kind of thing it is.
//
// It replaced (title string, year int) when television arrived, and replacing
// rather than adding a second method was the point. The media type is not
// expressible inside a title — the discriminator is TPB's cat= parameter and the
// keyword string 1337x is handed, neither of which can be recovered from a title
// however it is spelled. An indexer that never learns the media type cannot
// answer a television search at all: it answers a FILM search that happens to
// carry a show's name, and reports the nothing it finds as ok:true, count:0.
type Query struct {
	Title string

	// Year is the film's release year, and for television it is ignored — see
	// searchYear, which is where the measurement behind that lives.
	Year int

	// Media is MediaMovie or MediaTV. The zero value is the empty string and
	// means MediaMovie: every search made before this field existed was a film
	// search, and a Query{Title: "Interstellar", Year: 2014} must keep meaning
	// what it meant.
	Media string

	// Season is the season a television query is about, and 0 means no season
	// constraint. Only 1337x puts it in the query it sends — see searchQuery for
	// where it goes and tpbCategories for why TPB deliberately does not.
	Season int

	// Episode is the episode within Season, and 0 means the whole season.
	//
	// It is meaningless without a season and is IGNORED when Season is 0 rather
	// than treated as "episode 5 of any season": an episode number identifies an
	// episode only inside a season, and every release name in the wild spells it
	// that way too (S02E05, never a bare E05). internal/api refuses the
	// combination at the edge with a 400 so a caller finds out; here it is simply
	// not a constraint, because a silent half-filter is worse than neither.
	Episode int

	// IMDBID is the title's IMDb id as TMDB reports it — "tt14688458", prefix
	// and all — and empty means the caller did not have one.
	//
	// It is not a narrowing parameter like Season and Episode. It is a second,
	// stronger way to NAME the thing being searched for, and one indexer cannot
	// be searched without it: EZTV is keyed by id and has no keyword surface at
	// all. Every other indexer ignores it, which is why it is optional and why
	// its absence is not an error — it makes one source unaskable rather than
	// the search unanswerable. See QueryCapable.
	//
	// Carried in TMDB's spelling rather than EZTV's bare digits, so this stays
	// a fact about the title instead of one indexer's URL format leaking into
	// every Query. The strip happens at that indexer's boundary.
	IMDBID string
}

// IsTV reports whether q asks for television.
func (q Query) IsTV() bool { return q.Media == MediaTV }

// mediaType is q.Media with the zero value resolved to MediaMovie, so that the
// cache key, the capability check and the outcome all see one spelling of the
// default rather than each defaulting on its own.
func (q Query) mediaType() string {
	if q.Media == "" {
		return MediaMovie
	}
	return q.Media
}

// searchYear is the year an indexer should actually search with, which for
// television is none.
//
// A show's year is its FIRST AIR year, and an episode's name carries its own air
// year, so the two disagree by design: "Severance.S02E05.2025..." searched as
// Severance + 2019 is a release the year rules out. Both places the year is used
// get that wrong in their own way, and one zero fixes both without a branch in
// either:
//
//   - TPB drops a release whose name states a year different from the one
//     searched (tpbNameAllowsYear). The filter cannot even help here: a show's
//     first-air year appears in essentially no release name, so it can never
//     confirm a row and only ever fires on rows stating their own correct year.
//   - 1337x appends the year to its keyword string, and "Severance 2019" scores
//     near zero against releases named S02E05.
//
// The cost is that Release.Year is 0 on every television release rather than the
// show's first-air year. That is the honest value: what a release is stamped
// with is the year that was searched for (see tpbToReleases and toReleases), and
// an episode's year is not the show's.
func (q Query) searchYear() int {
	if q.IsTV() {
		return 0
	}
	return q.Year
}

// Release is one candidate download found at an indexer.
//
// Magnet may be empty: 1337x keeps magnets on per-torrent detail pages, so a
// search returns releases without them and a MagnetResolver fills one in for the
// release the user actually picks.
type Release struct {
	Title     string
	Year      int
	Quality   string // "1080p", "2160p"
	SizeBytes int64
	Seeders   int
	Magnet    string
	Indexer   string

	// Season and Episode are read out of the release name, and 0 means the name
	// does not say. They are a property of the NAME rather than of the query, so
	// they are parsed on every release from every media type: a season pack
	// ("Severance - Season 2 - ...") has a Season and no Episode, a single
	// episode ("Severance S02E05 ...") has both, and a film has neither.
	Season  int
	Episode int

	// detailPath is where the magnet lives when Magnet is empty — for 1337x, the
	// site-relative path of the torrent's detail page. Unexported because the
	// documented Release has exactly the fields above; it is an implementation
	// detail of lazy resolution, and only the indexer that produced the Release
	// knows how to read it back.
	detailPath string
}

// Indexer is a source of releases. Searches run concurrently across indexers and
// a failing one is omitted rather than fatal, so implementations should return an
// error rather than partial nonsense.
type Indexer interface {
	Name() string
	Search(ctx context.Context, q Query) ([]Release, error)
}

// MagnetResolver is implemented by indexers whose search results do not carry
// magnets. 1337x is the reason it exists: its magnets are on detail pages, so
// eagerly resolving a 20-result search would mean 21 protected requests through
// minter at ~9 s each. Only the release a user picks costs the second fetch.
type MagnetResolver interface {
	ResolveMagnet(ctx context.Context, r Release) (string, error)
}

// MediaCapable is implemented by indexers that do NOT handle every media type.
// It sits beside Indexer rather than inside it for the same reason
// MagnetResolver does: it describes one source's limitation, and making every
// implementation answer a question only one of them has is how an interface
// starts collecting the union of its implementations.
//
// Not implementing it means "handles everything", which is why YTS is the only
// indexer that says anything at all: it is /list_movies.json and has no
// television surface. TPB and 1337x are general catalogues and answer both.
type MediaCapable interface {
	Handles(media string) bool
}

// handlesMedia reports whether ix will answer a query for this media type.
//
// The default is the whole design: an indexer that declares nothing handles
// everything, so adding a fourth source costs no capability declaration unless
// it actually lacks one. That default is also what makes Cache able to forward
// Handles, which is the difference from resolverFor — see Cache.Handles.
func handlesMedia(ix Indexer, media string) bool {
	if c, ok := ix.(MediaCapable); ok {
		return c.Handles(media)
	}
	return true
}

// QueryCapable is implemented by indexers that cannot answer every query of a
// media type they otherwise handle. It sits beside MediaCapable and is a
// strictly narrower question: MediaCapable asks whether this KIND of thing is
// in the catalogue at all, this asks whether THIS query is one the source can
// be asked.
//
// EZTV is why it exists, and one interface could not have covered both. It has
// television and nothing but television — that is MediaCapable — and it is
// keyed by IMDb id rather than by title, so a television search carrying no id
// is a question it has no way to put. The two are independent: a query can pass
// one and fail the other, and it is the pair that decides whether the indexer
// is asked.
//
// A source that declines this way is reported NotApplicable rather than failed,
// which is the same treatment YTS gets on a television search. Nothing is
// wrong, nothing is unconfigured, and there is nothing for anybody to do.
type QueryCapable interface {
	Answers(q Query) bool
}

// answersQuery reports whether ix can answer this particular query.
//
// The default is true, and it is handlesMedia's default for handlesMedia's
// reason: an indexer that declares nothing answers everything, so this costs
// existing sources no declaration and costs a new one nothing unless it
// genuinely has a limit. That default is also what lets Cache forward it — see
// Cache.Answers.
func answersQuery(ix Indexer, q Query) bool {
	if c, ok := ix.(QueryCapable); ok {
		return c.Answers(q)
	}
	return true
}
