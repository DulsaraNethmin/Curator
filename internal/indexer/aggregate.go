package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// ErrReleaseExpired reports that an id cannot be resolved: either its search has
// aged out or it was never issued. The two are deliberately not distinguished —
// both mean "search again", and telling a caller which of the two it was would
// disclose whether an id it guessed had ever existed.
//
// It is a sentinel so the API layer can answer 410 Gone rather than a vague 500.
var ErrReleaseExpired = errors.New("release id unknown or its search has expired")

// Found is a release after merging: the release itself, the opaque id a magnet is
// later resolved by, and every indexer that carried it.
//
// Release itself is left alone — it is phase 1's ported shape and describes what a
// single indexer returned. Identity and provenance are properties of the merge,
// so they live here.
type Found struct {
	Release
	ID string
	// Indexers is every indexer that carried this release, in the order they were
	// configured. More than one means the copies deduplicated onto a shared info
	// hash.
	Indexers []string

	// owner is the indexer whose copy of this release survived dedup — the only
	// one that can read its detail path back. It is tracked separately from
	// Indexers because dedup keeps the copy with the most seeders while merging
	// the names, so Indexers[0] is whichever indexer was seen first and need not
	// be the one this release actually came from.
	owner string
}

// Outcome is one indexer's report from a search. It exists so that "a failing
// indexer is omitted, never fatal" cannot decay into "a failing indexer is
// invisible" — which is minter's 200-carrying-a-failure bug wearing a different
// hat. A search where 1337x is down is still a success, and still says so.
type Outcome struct {
	Name  string
	OK    bool
	Count int
	Error string

	// Unconfigured says the indexer did not fail so much as never start: the
	// companion service it needs is not answering. Today that is 1337x and
	// minter, which lives behind compose's 1337x profile.
	//
	// It is a separate field from OK because the two lead to different actions
	// and only one of them is the user's. A failed indexer is something to
	// retry or wait out; an unconfigured one is a container that was never
	// started, and the answer is one pasted line
	// (docs/tasks/T49-minter-on-demand.md). Folding it into Error would leave
	// the screen matching on a message, which is how this stops being true.
	//
	// An indexer that is switched OFF never appears here at all — it is not
	// constructed, so it runs no search and files no outcome. This field is
	// only ever about one that is on and cannot work.
	Unconfigured bool

	// NotApplicable says the indexer was never asked, because the media type is
	// not one it has. YTS is the case it exists for, and the argument is
	// Unconfigured's argument twice over.
	//
	// Without it a television search reaches YTS, which is /list_movies.json and
	// has no television surface at all, and comes back with an empty slice and a
	// nil error — which YTS's own doc comment defines as "YTS does not have this
	// film". The aggregator would then report ok:true, count:0. It would not
	// crash and it would not fail; it would quietly lie, in exactly the format
	// the code documents as indistinguishable from "nobody uploaded it".
	//
	// Separate from OK because the three states lead to three different actions
	// and only one of them is anybody's to take: a failed indexer is something to
	// retry, an unconfigured one is a container to start, and this one is neither
	// — it is a source that will never have television, and nothing is wrong.
	//
	// It is reported rather than omitted because Outcomes is positional against
	// the configured indexers, and because a source that was not asked is a fact
	// about the search worth stating once.
	NotApplicable bool
}

// SearchResult is one whole search: the merged, ranked releases and what each
// indexer had to say.
type SearchResult struct {
	Releases []Found
	Outcomes []Outcome
}

// Aggregator searches every indexer at once and merges the results. It does no
// HTTP itself — the indexers do that.
type Aggregator struct {
	indexers []Indexer
	timeout  time.Duration

	// ttl bounds how long an id stays resolvable. It matches the search cache's
	// TTL: an id is only useful while the search that issued it is still around,
	// because resolving one means reading back the unexported detail path that
	// search recorded. Without it, ResolveMagnet would either grow a map forever
	// or silently re-search, and re-searching answers a different question than
	// the one the caller asked.
	ttl time.Duration

	// now is injected so expiry can be tested by moving a clock rather than by
	// sleeping.
	now func() time.Time

	mu   sync.Mutex
	byID map[string]issued
}

// issued is a release handed out under an id, with the indexer that produced it —
// the only one that knows how to read its detail path back.
type issued struct {
	release Found
	indexer Indexer
	expires time.Time
}

// NewAggregator returns an Aggregator over indexers. timeout bounds a whole
// search; ttl is how long the ids it stamps stay resolvable, and should match the
// search cache's TTL.
func NewAggregator(indexers []Indexer, timeout, ttl time.Duration) *Aggregator {
	return &Aggregator{
		indexers: indexers,
		timeout:  timeout,
		ttl:      ttl,
		now:      time.Now,
		byID:     make(map[string]issued),
	}
}

// Search searches every indexer concurrently and returns the merged, ranked
// releases with a per-indexer outcome for each.
//
// The error return is for the caller's own context being cancelled. An indexer
// failing — even all of them — is not an error: it is reported in Outcomes, and
// the search still succeeded.
func (a *Aggregator) Search(ctx context.Context, q Query) (SearchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()

	// The indexers are asked for a normalised title; everything the caller sees
	// keeps the canonical one (docs/decisions.md D20). A colon costs 1337x every
	// result it would have returned, silently.
	//
	// Here rather than inside x1337.searchQuery, which is the narrower place:
	// indexer.Cache wraps 1337x and keys on the query it is HANDED, so
	// normalising below the cache leaves "avengers: endgame" and "avengers
	// endgame" as two entries for one identical minter fetch, and each path pays
	// its own ~9 s browser launch. Above the cache, the key is the string that
	// was actually queried.
	//
	// The season deliberately stays in the Query rather than being folded into
	// the title here: each indexer spells it differently, and one of them
	// (TPB, measured) is better off not spelling it at all.
	q.Title = NormaliseQuery(q.Title)

	// The default resolved once, so that the capability check, the cache key and
	// the indexers themselves all see the same word for it.
	q.Media = q.mediaType()

	// Results land in per-indexer slots rather than an append-shared slice so that
	// a straggler still running after the deadline has somewhere defined to write.
	type slot struct {
		reported bool
		releases []Release
		err      error
	}
	slots := make([]slot, len(a.indexers))
	var mu sync.Mutex

	// Which indexers this search is not for, decided BEFORE the fan-out and
	// remembered. Not deciding it inside the loop and simply skipping the g.Go:
	// a slot with no goroutine never sets reported, and the reporting switch
	// below opens with `case !s.reported:` producing "timed out after 30s" — so
	// YTS would be reported as having timed out on every television search.
	//
	// Two questions, not one, and they are independent. handlesMedia asks
	// whether this KIND of thing is in the source's catalogue — YTS has no
	// television. answersQuery asks whether THIS query is one the source can be
	// asked at all — EZTV is keyed by IMDb id, so a television search carrying
	// none is unaskable there even though television is all it has. Both land
	// in the same skip, and so in the same NotApplicable outcome, because from
	// the screen's side they are the same fact: this source was not asked, and
	// nothing is wrong.
	skip := make([]bool, len(a.indexers))
	for i, ix := range a.indexers {
		skip[i] = !handlesMedia(ix, q.Media) || !answersQuery(ix, q)
	}

	// A plain errgroup.Group, deliberately not errgroup.WithContext: WithContext
	// cancels every sibling the moment one goroutine returns non-nil, which is
	// exactly backwards here — one downed indexer would silently empty an
	// otherwise good search. Each goroutine records its own outcome and returns
	// nil, so nothing is ever cancelled on a sibling's behalf.
	var g errgroup.Group
	for i, ix := range a.indexers {
		if skip[i] {
			continue
		}
		g.Go(func() error {
			releases, err := ix.Search(ctx, q)
			mu.Lock()
			defer mu.Unlock()
			slots[i] = slot{reported: true, releases: releases, err: err}
			return nil
		})
	}

	// Wait for the fan-out, but not past the deadline: an indexer that ignores its
	// context must not hold the whole search open. Whatever has reported by then is
	// what the search returns.
	done := make(chan struct{})
	go func() {
		_ = g.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}

	mu.Lock()
	snapshot := make([]slot, len(slots))
	copy(snapshot, slots)
	mu.Unlock()

	out := SearchResult{Outcomes: make([]Outcome, len(a.indexers))}
	var all []Found
	for i, ix := range a.indexers {
		name := ix.Name()
		s := snapshot[i]
		switch {
		case skip[i]:
			// First, ahead of the timeout arm, because this slot was never
			// given a goroutine and so can never have reported. The outcome is
			// still filed rather than omitted: Outcomes is positional against
			// a.indexers, and an omitted slot would render as a nameless
			// {"name":"","ok":false} on the search screen.
			out.Outcomes[i] = Outcome{Name: name, NotApplicable: true}
		case !s.reported:
			out.Outcomes[i] = Outcome{Name: name, OK: false, Error: fmt.Sprintf("timed out after %s", a.timeout)}
		case s.err != nil:
			// errors.Is rather than a type switch on the indexer: 1337x is the
			// only one needing a companion service today, and keying on the
			// sentinel means the second one to need one gets this for free
			// instead of adding a name to a list here.
			out.Outcomes[i] = Outcome{
				Name: name, OK: false, Error: s.err.Error(),
				Unconfigured: errors.Is(s.err, ErrUnreachable),
			}
		default:
			out.Outcomes[i] = Outcome{Name: name, OK: true, Count: len(s.releases)}
			for _, r := range s.releases {
				all = append(all, Found{Release: r, ID: releaseID(r), Indexers: []string{name}, owner: name})
			}
		}
	}

	out.Releases = rank(narrow(dedupe(all), q), q)
	a.issue(out.Releases)
	return out, nil
}

// indexerByName maps an indexer's name back to the indexer itself. Resolution
// needs the concrete indexer that produced a release — only it knows how to read
// its detail path back — while a merged release carries just the names.
func (a *Aggregator) indexerByName(name string) Indexer {
	for _, ix := range a.indexers {
		if ix.Name() == name {
			return ix
		}
	}
	return nil
}

// issue records the ids this search handed out so ResolveMagnet can read them back,
// and prunes anything already expired. Pruning happens here, on write, rather than
// in a background goroutine: this is a single-user service with a handful of
// searches an hour, and a janitor ticker that outlives its owner is a leak waiting
// to be found in phase 6.
func (a *Aggregator) issue(found []Found) {
	now := a.now()
	expires := now.Add(a.ttl)

	a.mu.Lock()
	defer a.mu.Unlock()
	for id, e := range a.byID {
		if !e.expires.After(now) {
			delete(a.byID, id)
		}
	}
	for _, f := range found {
		ix := a.indexerByName(f.owner)
		if ix == nil {
			continue
		}
		a.byID[f.ID] = issued{release: f, indexer: ix, expires: expires}
	}
}

// Release returns the release a stamped id refers to, if its search has not
// expired.
//
// It exists so a caller recording a download can read the indexer and release
// name off the server's own copy instead of accepting them from the client. That
// is the same reasoning as D10: the client already had to be told the id, and
// letting it hand back facts we already hold is how a request starts describing
// itself.
func (a *Aggregator) Release(id string) (Found, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	e, ok := a.byID[id]
	if !ok || !e.expires.After(a.now()) {
		return Found{}, false
	}
	return e.release, true
}

// ResolveMagnet returns the magnet for a stamped id.
//
// YTS and TPB releases already carry theirs and cost nothing. A 1337x release is
// fetched from its detail page at this point and not before — that is what keeps a
// 20-result search one protected request through minter rather than 21, at ~9 s
// each.
func (a *Aggregator) ResolveMagnet(ctx context.Context, id string) (string, error) {
	a.mu.Lock()
	e, ok := a.byID[id]
	expired := ok && !e.expires.After(a.now())
	if expired {
		delete(a.byID, id)
	}
	a.mu.Unlock()

	if !ok || expired {
		return "", fmt.Errorf("resolve %s: %w", id, ErrReleaseExpired)
	}
	if e.release.Magnet != "" {
		return e.release.Magnet, nil
	}

	resolver, ok := resolverFor(e.indexer)
	if !ok {
		return "", fmt.Errorf("resolve %s: indexer %s has no magnet for it and cannot resolve one", id, e.indexer.Name())
	}
	magnet, err := resolver.ResolveMagnet(ctx, e.release.Release)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", id, err)
	}
	return magnet, nil
}

// resolverFor finds the MagnetResolver behind an indexer, reaching through any
// decorators wrapping it.
//
// A plain type assertion is not enough, and the case it misses is the one that
// matters: 1337x is the only indexer needing resolution and the only one wrapped
// in a Cache, and a Cache is deliberately not a MagnetResolver — Go method sets
// are not conditional, so forwarding the method would make a Cache around YTS
// claim to resolve magnets it never has to. Asserting on the outer value alone
// would mean composing the cache silently disabled lazy resolution, turning every
// 1337x pick into "no magnet" with nothing to point at.
func resolverFor(ix Indexer) (MagnetResolver, bool) {
	for ix != nil {
		if r, ok := ix.(MagnetResolver); ok {
			return r, true
		}
		u, ok := ix.(interface{ Unwrap() Indexer })
		if !ok {
			return nil, false
		}
		inner := u.Unwrap()
		if inner == ix {
			// A decorator returning itself would spin here forever.
			return nil, false
		}
		ix = inner
	}
	return nil, false
}

// releaseID is the opaque, deterministic id a release is identified by:
// sha256(indexer + "\x00" + title + "\x00" + detailPath|magnet), first 8 bytes,
// hex.
//
// Deterministic so the same release gets the same id on every search, and opaque
// so it says nothing about how the indexer works. The alternative — exporting the
// detail path so the client hands it back — would mean the API accepting a URL
// from a caller and passing it to minter to fetch, which is request forgery
// through a service built to fetch convincingly. See D10.
func releaseID(r Release) string {
	locator := r.detailPath
	if locator == "" {
		locator = r.Magnet
	}
	sum := sha256.Sum256([]byte(r.Indexer + "\x00" + r.Title + "\x00" + locator))
	return hex.EncodeToString(sum[:8])
}

// dedupe collapses releases sharing an info hash, keeping the copy with the most
// seeders and recording every indexer that carried it.
//
// A release with no hash — every unresolved 1337x row, since its magnet is still
// on a detail page — cannot be deduplicated against and passes through untouched.
// A near-duplicate row is the honest outcome; guessing by name similarity would
// merge genuinely different releases and is not worth the one row it saves.
func dedupe(in []Found) []Found {
	out := make([]Found, 0, len(in))
	at := make(map[string]int, len(in))

	for _, f := range in {
		hash := InfoHash(f.Magnet)
		if hash == "" {
			out = append(out, f)
			continue
		}
		i, seen := at[hash]
		if !seen {
			at[hash] = len(out)
			out = append(out, f)
			continue
		}
		kept := out[i]
		names := appendMissing(kept.Indexers, f.Indexers...)
		if f.Seeders > kept.Seeders {
			// The higher seeder count is the more useful answer to "will this
			// finish", and it brings its own id and magnet with it.
			f.Indexers = names
			out[i] = f
			continue
		}
		kept.Indexers = names
		out[i] = kept
	}
	return out
}

func appendMissing(dst []string, add ...string) []string {
	for _, s := range add {
		found := false
		for _, existing := range dst {
			if existing == s {
				found = true
				break
			}
		}
		if !found {
			dst = append(dst, s)
		}
	}
	return dst
}

// FilterFound keeps only merged releases whose quality matches one of want. An
// empty want keeps everything.
//
// It defers to the ported FilterQuality one release at a time rather than
// reimplementing the match, so the spellings a caller may type — "1080" without
// the p, "4k" for 2160p — keep a single definition.
func FilterFound(in []Found, want []string) []Found {
	if len(want) == 0 {
		return in
	}
	out := make([]Found, 0, len(in))
	for _, f := range in {
		if len(FilterQuality([]Release{f.Release}, want)) == 1 {
			out = append(out, f)
		}
	}
	return out
}

// narrow drops releases whose own name states a season or episode that is not
// the one asked for. Everything else survives, including names that state
// nothing.
//
// It exists because until now a season NARROWED ONLY 1337x — the season went
// into that indexer's keyword string and nothing anywhere filtered a result — so
// asking TPB for season 2 returned all four seasons and the two backends
// answered different questions from one Query. Filtering here rather than in
// each indexer is what makes them agree, and it is the only place that can:
// TPB's query deliberately does not carry the season (see tpbCategories), so its
// rows can only be narrowed after they arrive.
func narrow(in []Found, q Query) []Found {
	if !q.IsTV() || q.Season == 0 {
		return in
	}
	out := make([]Found, 0, len(in))
	for _, f := range in {
		if SeasonTier(f.Release, q) != TierWrong {
			out = append(out, f)
		}
	}
	return out
}

// rank orders releases by how directly they answer the query, then by seeders
// descending, ties broken by quality then name.
//
// Not by quality first: a 1-seeder 2160p above a 500-seeder 1080p is the wrong
// answer to the only question a manual picker is actually asking, which is whether
// this will finish downloading. Seeders predict that; resolution does not. Quality
// is offered as a filter instead — see D11. The tie-breaks make the order total,
// so a test can assert it exactly.
//
// The tier sits ABOVE seeders, and only that ordering survives the measurement
// this was built against: the 727-seeder Severance season pack outseeds every
// single episode apibay carries by roughly two to one, so ranking by seeders
// first buries the episode somebody explicitly asked for underneath the pack
// they did not. Below the tier the old order is untouched, and for every query
// that names no season SeasonTier is TierExact for everything — so a film search
// sorts today exactly as it did before this existed.
func rank(in []Found, q Query) []Found {
	sort.SliceStable(in, func(i, j int) bool {
		a, b := in[i], in[j]
		if ta, tb := SeasonTier(a.Release, q), SeasonTier(b.Release, q); ta != tb {
			return ta < tb
		}
		if a.Seeders != b.Seeders {
			return a.Seeders > b.Seeders
		}
		ra, rb := QualityRank(a.Quality), QualityRank(b.Quality)
		if ra != rb {
			return ra < rb
		}
		return strings.ToLower(a.Title) < strings.ToLower(b.Title)
	})
	return in
}
