// Package store is curator's SQLite persistence: the schema and the hand-written
// queries phase 1 needs.
//
// It knows about rows and nothing else. It does not import internal/library or
// internal/tmdb, and it never reads a disk or calls an API — callers map what
// they found into these types and hand them over.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	// Pure-Go SQLite (decisions.md D4). The cgo driver mattn/go-sqlite3 would
	// make GOARCH=arm64 go build need a cross-compilation toolchain; this one
	// keeps shipping to the Pi a single command. Note it registers itself under
	// the name "sqlite", not "sqlite3".
	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// Store owns the database handle and every statement run against it.
type Store struct {
	db *sql.DB

	// now is the clock added_at is read from. A field rather than a direct
	// time.Now call so tests can freeze it; production never sets it.
	now func() time.Time
}

// Open opens the SQLite database at path, creating the file if it is absent, and
// applies the schema. Applying it to an already-populated database is safe and
// does nothing — every statement in schema.sql is IF NOT EXISTS — so this is the
// whole of "migrate on startup" for phase 1.
func Open(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	// sql.Open is lazy: it validates neither the DSN nor the file. Ping forces a
	// real connection so a bad path fails here rather than at the first query.
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	s := &Store{db: db, now: func() time.Time { return time.Now().UTC() }}
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema to %s: %w", path, err)
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close database: %w", err)
	}
	return nil
}

// dsn builds the modernc.org/sqlite connection string.
//
// The pragmas ride in the DSN rather than being executed once after opening
// because foreign_keys is a *per-connection* setting and database/sql pools
// connections: a lone `PRAGMA foreign_keys = ON` would apply to whichever pooled
// connection happened to serve it and silently not to the next, leaving the
// downloads → movies reference decorative exactly as T2 warns. journal_mode is
// persistent in the file itself, but is set the same way for symmetry.
//
// _txlock=immediate takes the write lock when a transaction begins instead of at
// its first write, so the read-then-write in UpsertMovieByPath cannot fail on a
// lock upgrade.
func dsn(path string) string {
	q := url.Values{
		"_busy_timeout": {"5000"},
		"_foreign_keys": {"1"},
		"_journal_mode": {"WAL"},
		"_txlock":       {"immediate"},
	}
	return "file:" + uriPath(path) + "?" + q.Encode()
}

// uriPath percent-encodes path for a file: URI without escaping the separators,
// so a database under a directory containing a space or a '?' still opens —
// SQLite splits the DSN at the first unescaped '?' and decodes the rest.
func uriPath(path string) string {
	segments := strings.Split(filepath.ToSlash(path), "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

// timeLayout is RFC 3339 with a fixed nine-digit fraction, always written in UTC.
//
// Fixed width is load-bearing: DATETIME in SQLite is an affinity, not a type, so
// these are text and ORDER BY compares them as text. time.RFC3339Nano trims
// trailing zeros from the fraction, which would sort "…:00.5Z" *before* "…:00Z"
// because '.' < 'Z'.
//
// Times are formatted here rather than passed to the driver as time.Time because
// modernc.org/sqlite writes a bare time.Time with time.Time.String() —
// "2026-08-12 14:01:00.1 +0530 IST" — which is neither sortable nor reliably
// round-trippable.
const timeLayout = "2006-01-02T15:04:05.000000000Z07:00"

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

// asTime converts whatever the driver hands back for a DATETIME column.
//
// The usual case is time.Time: modernc.org/sqlite inspects the *declared* column
// type and parses TEXT in a DATE/DATETIME/TIMESTAMP column into a time.Time
// before database/sql ever sees it. Scanning such a column into a *string
// therefore does not return what was written — database/sql reformats the
// time.Time with RFC3339Nano, silently trimming the fixed-width fraction. The
// string branches keep the read working if that behaviour changes, and cover a
// value written into the file by some other tool.
func asTime(v any) (time.Time, error) {
	switch t := v.(type) {
	case time.Time:
		return t.UTC(), nil
	case string:
		return parseTime(t)
	case []byte:
		return parseTime(string(t))
	default:
		return time.Time{}, fmt.Errorf("unexpected timestamp type %T", v)
	}
}

func parseTime(s string) (time.Time, error) {
	for _, layout := range []string{timeLayout, time.RFC3339Nano} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("parse timestamp %q", s)
}
