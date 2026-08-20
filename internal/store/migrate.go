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
	if err := s.addColumn(ctx, "movies", "tmdb_year", "INTEGER"); err != nil {
		return err
	}

	// T88. A show's TMDB id, in its own column because TMDB's movie and tv id
	// sequences overlap and `tmdb_id` is UNIQUE — store.tmdbColumn has the whole
	// argument. Nullable, and NULL for every film, so it backfills to exactly the
	// right answer with no backfill.
	if err := s.addColumn(ctx, "movies", "tmdb_tv_id", "INTEGER"); err != nil {
		return err
	}
	// The uniqueness `tmdb_id` gets from its column declaration, which a column
	// added by ALTER TABLE cannot have. **It cannot live in schema.sql either**:
	// store.go execs schema.sql BEFORE calling migrate, so on every existing
	// database the index would be created against a column that does not exist
	// yet and fail with "no such column" — on exactly the databases this
	// mechanism exists to serve.
	return s.addIndex(ctx, "movies_tmdb_tv_id", "movies(tmdb_tv_id)")
}

// addIndex creates a unique index if it is not already there.
//
// It asks the database nothing, unlike addColumn, because CREATE UNIQUE INDEX IF
// NOT EXISTS is already idempotent by construction — which is the property the
// doc comment above demands of every step, arrived at directly rather than
// through a shape inspection.
//
// name and target are compile-time constants from the line above, interpolated
// for the same reason addColumn interpolates: SQLite cannot bind an identifier.
// They must never become anything a caller supplies.
//
// A UNIQUE index over a nullable column is the right instrument here: SQLite
// treats NULLs as distinct, so every film — all of which have tmdb_tv_id NULL —
// coexists happily, and only two rows claiming the SAME show are refused.
func (s *Store) addIndex(ctx context.Context, name, target string) error {
	if _, err := s.db.ExecContext(ctx,
		fmt.Sprintf(`CREATE UNIQUE INDEX IF NOT EXISTS %s ON %s`, name, target),
	); err != nil {
		return fmt.Errorf("add index %s: %w", name, err)
	}
	return nil
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
