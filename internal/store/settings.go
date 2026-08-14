package store

import (
	"context"
	"fmt"
)

// Settings reads the whole settings table.
//
// The whole of it, because there are a few dozen keys and every caller wants
// all of them: start-up resolves the lot at once, and the settings screen
// renders the lot at once. A per-key getter would be a second query shape for
// no second question.
//
// Values are opaque strings. This package does not know which of them are
// secrets, which are durations, or which one is a WireGuard config — the
// encryption lives in internal/secret and the meaning in internal/settings,
// because this package knows about rows and nothing else.
func (s *Store) Settings(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("read settings: %w", err)
		}
		out[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	return out, nil
}

// SetSettings writes every key in set and removes every key in clear, in ONE
// transaction.
//
// One transaction is the whole design. A settings form with eight changed
// fields either applies all eight or none of them: half a configuration is not
// a state anything downstream reasons about, and the half that landed would be
// the half nobody remembers.
//
// Clearing is a deletion rather than an empty value, so "present in this table"
// and "configured" are the same fact. It is a separate argument from set rather
// than an empty string inside it because DOWNLOADS_PATH proves empty can be a
// deliberate value ("use the path qBittorrent reported verbatim"), and a store
// that cannot tell erase from set-to-empty would make that unsettable.
func (s *Store) SetSettings(ctx context.Context, set map[string]string, clear []string) error {
	if len(set) == 0 && len(clear) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed

	for key, value := range set {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO settings (key, value) VALUES (?, ?)
			ON CONFLICT(key) DO UPDATE SET value = excluded.value
		`, key, value); err != nil {
			// The key names the failure and the value never does: this error is
			// on its way to a log, and the value may be a WireGuard private key
			// (docs/decisions.md D28).
			return fmt.Errorf("write setting %s: %w", key, err)
		}
	}
	for _, key := range clear {
		if _, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key); err != nil {
			return fmt.Errorf("clear setting %s: %w", key, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	return nil
}
