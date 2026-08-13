package qbit

import (
	"path/filepath"
	"testing"
)

// --- path translation --------------------------------------------------------
//
// This table moved here from internal/importer in phase 6, unchanged. The
// backend that has a filesystem of its own is the one that knows how to map it
// onto curator's; the importer now receives a path it can open (D22).

func TestTranslate(t *testing.T) {
	cases := []struct {
		name    string
		paths   Paths
		in      string
		want    string
		wantErr bool
	}{{
		// The laptop case, and the case where curator shares the mount. No
		// configuration at all, and the path is used exactly as reported.
		name:  "unset passes through verbatim",
		paths: Paths{},
		in:    "/downloads/complete/curator/Movie.2014",
		want:  "/downloads/complete/curator/Movie.2014",
	}, {
		name:  "configured rewrites the prefix",
		paths: Paths{Curator: "/media/storage/media/downloads", QBit: "/downloads"},
		in:    "/downloads/complete/curator/Movie.2014",
		want:  "/media/storage/media/downloads/complete/curator/Movie.2014",
	}, {
		name:  "the root itself",
		paths: Paths{Curator: "/media/downloads", QBit: "/downloads"},
		in:    "/downloads",
		want:  "/media/downloads",
	}, {
		name:  "a trailing slash on the configured root",
		paths: Paths{Curator: "/media/downloads", QBit: "/downloads/"},
		in:    "/downloads/complete/x",
		want:  "/media/downloads/complete/x",
	}, {
		// Set but not matching is an error, not a pass-through: someone
		// configured a translation and it did not apply.
		name:    "a path outside the configured root",
		paths:   Paths{Curator: "/media/downloads", QBit: "/downloads"},
		in:      "/var/lib/torrents/Movie.2014",
		wantErr: true,
	}, {
		// The boundary check. A prefix comparison without it maps /downloads2
		// into the middle of the library.
		name:    "a sibling directory sharing the prefix",
		paths:   Paths{Curator: "/media/downloads", QBit: "/downloads"},
		in:      "/downloads2/complete/x",
		wantErr: true,
	}, {
		// An empty content path is NORMAL here, unlike in the importer: it is
		// what qBittorrent reports for a torrent whose metadata has not
		// arrived. Refusing to import from one stays the importer's job, where
		// the question is being asked for a reason.
		name:  "an empty content path passes through",
		paths: Paths{Curator: "/media/downloads", QBit: "/downloads"},
		in:    "",
		want:  "",
	}}

	for _, c := range cases {
		client := New("http://example", "u", "p", nil).WithPaths(c.paths)
		got, err := client.translate(c.in)
		switch {
		case c.wantErr && err == nil:
			t.Errorf("%s: translate(%q) = %q, want an error", c.name, c.in, got)
		case !c.wantErr && err != nil:
			t.Errorf("%s: translate(%q): %v", c.name, c.in, err)
		case !c.wantErr && got != filepath.FromSlash(c.want):
			t.Errorf("%s: translate(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}
