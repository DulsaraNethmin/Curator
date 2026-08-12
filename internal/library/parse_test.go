package library

import "testing"

func TestParseFolder(t *testing.T) {
	cases := []struct {
		name  string
		title string
		year  int
	}{
		// The plain cases.
		{"Interstellar (2014)", "Interstellar", 2014},
		{"Jaws (1975)", "Jaws", 1975},
		{"F1 (2025)", "F1", 2025},
		{"Iron Man 2 (2010)", "Iron Man 2", 2010},
		{"Deadpool & Wolverine (2024)", "Deadpool & Wolverine", 2024},
		{"Crime 101 (2026)", "Crime 101", 2026},

		// The trap: " - " stands in for a colon, but the hyphens inside
		// Spider-Man and X-Men are real. The title is handed back verbatim; any
		// rewriting is internal/tmdb's problem. See docs/decisions.md D9.
		{"Avengers - Infinity War (2018)", "Avengers - Infinity War", 2018},
		{"Captain America - The First Avenger (2011)", "Captain America - The First Avenger", 2011},
		{"Predator - Badlands (2025)", "Predator - Badlands", 2025},
		{"Spider-Man - Across the Spider-Verse (2023)", "Spider-Man - Across the Spider-Verse", 2023},
		{"Spider-Man - Into the Spider-Verse (2018)", "Spider-Man - Into the Spider-Verse", 2018},
		{"Spider-Man - No Way Home (2021)", "Spider-Man - No Way Home", 2021},
		{"Tom Clancy's Jack Ryan - Ghost War (2026)", "Tom Clancy's Jack Ryan - Ghost War", 2026},
		{"X-Men Origins - Wolverine (2009)", "X-Men Origins - Wolverine", 2009},

		// Parens inside the title survive; only the last group is the year.
		{"Movie (Director's Cut) (2019)", "Movie (Director's Cut)", 2019},
		// Extra whitespace around the year is tolerated, but nothing inside the
		// title is touched.
		{"The  Martian   (2015)", "The  Martian", 2015},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			title, year, ok := ParseFolder(tc.name)
			if !ok {
				t.Fatalf("ParseFolder(%q) = ok false, want true", tc.name)
			}
			if title != tc.title {
				t.Errorf("ParseFolder(%q) title = %q, want %q", tc.name, title, tc.title)
			}
			if year != tc.year {
				t.Errorf("ParseFolder(%q) year = %d, want %d", tc.name, year, tc.year)
			}
		})
	}
}

func TestParseFolderRejects(t *testing.T) {
	// A name that does not match is skipped and reported, never guessed at.
	names := []string{
		"",
		"Interstellar",              // no year at all
		"Interstellar 2014",         // year, but not parenthesised
		"Interstellar (2014",        // unclosed
		"Interstellar 2014)",        // unopened
		"(2014)",                    // no title
		"   (2014)",                 // whitespace is not a title
		"Interstellar (201)",        // three digits
		"Interstellar (20145)",      // five digits
		"Interstellar (20l4)",       // letter l, not a one
		"Interstellar (+201)",       // strconv.Atoi would have taken this
		"Interstellar ()",           // empty year
		"Interstellar (2014) 1080p", // trailing junk after the year
		".DS_Store",
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			title, year, ok := ParseFolder(name)
			if ok {
				t.Errorf("ParseFolder(%q) = (%q, %d, true), want ok false", name, title, year)
			}
		})
	}
}

// ParseFolder must never turn a hyphen into a colon, nor drop one.
func TestParseFolderDoesNotRewriteHyphens(t *testing.T) {
	const name = "Spider-Man - No Way Home (2021)"
	title, _, ok := ParseFolder(name)
	if !ok {
		t.Fatalf("ParseFolder(%q) failed", name)
	}
	if title != "Spider-Man - No Way Home" {
		t.Fatalf("title = %q, want %q verbatim", title, "Spider-Man - No Way Home")
	}
	for _, wrong := range []string{"Spider Man", "Spider-Man: No Way Home", "Spider Man - No Way Home", "Spider-Man - No Way Home:"} {
		if title == wrong {
			t.Fatalf("title was rewritten to %q", wrong)
		}
	}
}
