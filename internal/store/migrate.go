package store

import (
	"context"
	"fmt"
)

// Migrations, applied after schema.sql on every start.
//
// Five phases shipped without any of these, on a real argument: every statement
// in schema.sql is IF NOT EXISTS, so applying it every start is a no-op, and
// every column phases 3-6 needed already existed. `downloads.reason` does not,
// and CREATE TABLE IF NOT EXISTS silently does nothing to a table that is
// already there — a fresh clone would have the column and every existing
// database would not, which is the worst of the two outcomes because it is
// invisible here and only shows up on somebody else's machine.
//
// **Each step asks the database about its own shape rather than consulting a
// version number.** A version number is a second source of truth about
// something the database can already be asked for, and the failure it produces
// — a counter that says 3 against a schema that is at 2 — is unrecoverable
// without hand-editing the row that lied. Asking makes every step idempotent by
// construction and makes an interrupted migration a no-op rather than a state.
//
// The mechanism arrives in phase 7 rather than in phase 9, where it becomes
// unavoidable, because from phase 9 there are databases this repo has never
// seen. Introducing it with one nullable column that a test can prove from both
// directions is cheaper than introducing it under pressure.
func (s *Store) migrate(ctx context.Context) error {
	// T55 carries the stall reason into GET /api/downloads. T36 deliberately
	// stopped at a log line because the column was phase 7's; this is the
	// column, and it is deliberately the first passenger of the mechanism above
	// — nullable, unindexed, and read only with the row it belongs to.
	if err := s.addColumn(ctx, "downloads", "reason", "TEXT"); err != nil {
		return err
	}
	// T68. TMDB's release year for a row a human matched by hand, which is the
	// only kind of row whose own `year` — the folder's — can disagree with it.
	// Nullable on purpose: NULL means the two agree, which is every row the scan
	// matched, so this backfills to exactly the right answer with no backfill.
	return s.addColumn(ctx, "movies", "tmdb_year", "INTEGER")
}

// addColumn adds a column if the table does not already have one by that name.
//
// table, column and decl are compile-time constants from the line above — they
// are interpolated because SQLite cannot bind an identifier, and they must
// never become anything a caller supplies.
func (s *Store) addColumn(ctx context.Context, table, column, decl string) error {
	var found int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, table, column,
	).Scan(&found); err != nil {
		return fmt.Errorf("inspect %s: %w", table, err)
	}
	if found > 0 {
		return nil
	}
	if _, err := s.db.ExecContext(ctx,
		fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, column, decl),
	); err != nil {
		return fmt.Errorf("add %s.%s: %w", table, column, err)
	}
	return nil
}
