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
	SearchMovie(ctx context.Context, title string, year int) ([]Release, error)
}

// MagnetResolver is implemented by indexers whose search results do not carry
// magnets. 1337x is the reason it exists: its magnets are on detail pages, so
// eagerly resolving a 20-result search would mean 21 protected requests through
// minter at ~9 s each. Only the release a user picks costs the second fetch.
type MagnetResolver interface {
	ResolveMagnet(ctx context.Context, r Release) (string, error)
}
