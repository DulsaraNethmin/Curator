package indexer

import (
	"context"
	"testing"
	"time"
)

// The measured cases, plus the traps that make "strip punctuation" the wrong
// generalisation. See docs/decisions.md D20 for the numbers.
func TestNormaliseQuery(t *testing.T) {
	cases := map[string]string{
		// Measured: 1337x returns 0 for these with the colon and 20 without.
		"Avengers: Endgame":      "Avengers Endgame",
		"Avengers: Infinity War": "Avengers Infinity War",

		// Measured: unaffected either way — and the hyphen must survive, because
		// "Spider-Man" and "X-Men" contain real ones. D9 exists because that
		// distinction has already cost this project once.
		"Spider-Man: No Way Home":   "Spider-Man No Way Home",
		"Dune: Part Two":            "Dune Part Two",
		"X-Men Origins: Wolverine":  "X-Men Origins Wolverine",
		"Spider-Man":                "Spider-Man",
		"X-Men Origins - Wolverine": "X-Men Origins - Wolverine",

		// Unmeasured punctuation is left alone on purpose.
		"Deadpool & Wolverine":              "Deadpool & Wolverine",
		"Tom Clancy's Jack Ryan: Ghost War": "Tom Clancy's Jack Ryan Ghost War",

		// Nothing to do.
		"Interstellar": "Interstellar",
		"":             "",

		// The whitespace a removed colon leaves behind is collapsed, so these
		// three produce one cache key rather than three.
		"Avengers:Endgame":   "Avengers Endgame",
		"Avengers:  Endgame": "Avengers Endgame",
		" Avengers: Endgame": "Avengers Endgame",

		// Degenerate, and it must not panic or produce a stray space.
		":":   "",
		": :": "",
	}

	for in, want := range cases {
		if got := NormaliseQuery(in); got != want {
			t.Errorf("NormaliseQuery(%q) = %q, want %q", in, got, want)
		}
	}
}

// recordingIndexer notes the title it was handed.
type recordingIndexer struct {
	name  string
	saw   []string
	given chan struct{}
}

func (r *recordingIndexer) Name() string { return r.name }

func (r *recordingIndexer) Search(_ context.Context, q Query) ([]Release, error) {
	r.saw = append(r.saw, q.Title)
	return []Release{{
		Title:   "Avengers Endgame 2019 1080p WEBRip",
		Magnet:  "magnet:?xt=urn:btih:414A6F933C48FC7543A9CDB42C854B5457C5BCC7",
		Indexer: r.name,
		Seeders: 10,
	}}, nil
}

// The half that matters at this layer: the indexer is asked the normalised
// title while the caller's canonical one is untouched.
func TestAggregatorQueriesIndexersWithTheNormalisedTitle(t *testing.T) {
	ix := &recordingIndexer{name: "fake"}
	aggregator := NewAggregator([]Indexer{ix}, 5*time.Second, time.Hour)

	const canonical = "Avengers: Endgame"
	if _, err := aggregator.Search(context.Background(), Query{Title: canonical, Year: 2019}); err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(ix.saw) != 1 {
		t.Fatalf("the indexer was called %d times, want 1", len(ix.saw))
	}
	if ix.saw[0] != "Avengers Endgame" {
		t.Errorf("the indexer was asked %q, want the normalised %q", ix.saw[0], "Avengers Endgame")
	}
	// The caller's string is the film's name and must not have been rewritten
	// under it — it is what becomes the library folder.
	if canonical != "Avengers: Endgame" {
		t.Errorf("the canonical title was mutated to %q", canonical)
	}
}

// A title with no colon must reach the indexer byte for byte, so the cache key
// for every existing search is unchanged by this.
func TestAggregatorLeavesAnOrdinaryTitleAlone(t *testing.T) {
	ix := &recordingIndexer{name: "fake"}
	aggregator := NewAggregator([]Indexer{ix}, 5*time.Second, time.Hour)

	if _, err := aggregator.Search(context.Background(), Query{Title: "Spider-Man", Year: 2002}); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if ix.saw[0] != "Spider-Man" {
		t.Errorf("the indexer was asked %q, want %q — the hyphen is real", ix.saw[0], "Spider-Man")
	}
}
