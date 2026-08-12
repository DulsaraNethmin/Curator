// Package library turns the movie library on disk into records.
//
// The library is flat: one directory per movie, named "Title (Year)", holding the
// feature file and whatever has accumulated beside it. Nothing here talks to TMDB
// or to the database — Scan returns values and the caller decides what to persist.
package library

import "strings"

// ParseFolder splits a "Title (Year)" folder name. ok is false for any name that
// does not have a four-digit year in parentheses at the end, or that has nothing
// before it; such a name is skipped by Scan rather than guessed at.
//
// The title comes back exactly as it appears on disk — only the "(Year)" suffix and
// the whitespace before it are removed. It is NOT repaired. Colons are illegal in
// filenames and were replaced with " - " (8 of the 29 folders), but Spider-Man and
// X-Men contain real hyphens, and "Spider-Man - No Way Home (2021)" contains one of
// each, so any "-" -> ":" rewrite here would corrupt real titles. Rewriting belongs
// to internal/tmdb, as a fallback on an empty search result. See docs/decisions.md D9.
func ParseFolder(name string) (title string, year int, ok bool) {
	trimmed := strings.TrimSpace(name)
	if !strings.HasSuffix(trimmed, ")") {
		return "", 0, false
	}
	// Last "(" so that "Movie (Director's Cut) (2019)" keeps the earlier parens in
	// the title instead of losing them.
	open := strings.LastIndex(trimmed, "(")
	if open < 0 {
		return "", 0, false
	}

	y, ok := parseYear(trimmed[open+1 : len(trimmed)-1])
	if !ok {
		return "", 0, false
	}

	title = strings.TrimSpace(trimmed[:open])
	if title == "" {
		return "", 0, false
	}
	return title, y, true
}

// parseYear accepts exactly four ASCII digits. strconv.Atoi is deliberately not used:
// it would accept "+201" and reject nothing about length, and a folder named
// "Movie (+201)" is not a year.
func parseYear(s string) (int, bool) {
	if len(s) != 4 {
		return 0, false
	}
	year := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		year = year*10 + int(c-'0')
	}
	return year, true
}
