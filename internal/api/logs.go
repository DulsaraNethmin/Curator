package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/DulsaraNethmin/curator/internal/logs"
)

// defaultLogLimit is how many entries one poll returns when the caller does not
// say. It matches the buffer's own default, so a first load gets the whole tail
// in one request.
const defaultLogLimit = 1000

// LogTail is the in-memory tail of the process log. Declared here rather than
// taken as the concrete type, like every other dependency in this package.
type LogTail interface {
	Since(cursor uint64, limit int) ([]logs.Entry, uint64, uint64)
	Len() int
}

// WithLogs attaches the log tail and returns the server.
func (s *Server) WithLogs(tail LogTail) *Server {
	s.logs = tail
	return s
}

// RegisterLogs mounts the log route.
func (s *Server) RegisterLogs(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/logs", s.handleLogs)
}

// logsBody is what GET /api/logs answers.
//
// Cursor is the sequence the caller should send back next time, so a poll costs
// only what has happened since. Missed is how many entries fell off the ring
// before the caller asked for them — reported rather than papered over, because
// a log with a silent gap in it is worse than one that admits the gap.
type logsBody struct {
	Entries  []logs.Entry `json:"entries"`
	Cursor   uint64       `json:"cursor"`
	Missed   uint64       `json:"missed"`
	Buffered int          `json:"buffered"`
}

// handleLogs serves the tail of curator's own log.
//
// It is read-only and it is in memory: this reads what the process has already
// written, and cannot reach a file, a journal or anything else on the host. The
// buffer scrubs known secrets on the way in (docs/decisions.md D18), because
// there is no authentication in front of this and a log line here is a log line
// on the network.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if s.logs == nil {
		s.fail(w, http.StatusServiceUnavailable, errors.New("the log buffer is not configured"))
		return
	}

	query := r.URL.Query()

	cursor, err := parseCursor(query.Get("since"))
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	limit, err := parseLimit(query.Get("limit"))
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}

	entries, next, missed := s.logs.Since(cursor, limit)

	if level := strings.TrimSpace(query.Get("level")); level != "" {
		// Filtered after the cursor is computed, never before: the cursor has to
		// keep advancing past entries the caller filtered out, or a quiet period
		// of DEBUG lines would make every poll re-deliver the same tail.
		entries, err = filterByLevel(entries, level)
		if err != nil {
			s.fail(w, http.StatusBadRequest, err)
			return
		}
	}

	// After the cursor for the same reason level is, and after level because the
	// two compose: a VPN page wanting warnings from one subsystem asks for both.
	//
	// It filters on an ATTRIBUTE rather than a field, because that is where
	// log.With("subsystem", "vpn") lands (internal/logs teeHandler.Handle), and
	// therefore costs nothing in internal/vpn or internal/logs to produce.
	//
	// The trade-off it shares with level, stated once: limit is applied inside
	// Since, so a filter shrinks a page that was already capped. Twenty vpn
	// lines among a thousand come back as twenty, not as a thousand searched for
	// twenty.
	if subsystem := strings.TrimSpace(query.Get("subsystem")); subsystem != "" {
		entries = filterBySubsystem(entries, subsystem)
	}

	if entries == nil {
		entries = []logs.Entry{} // [] and never null; the UI iterates it
	}
	s.respond(w, http.StatusOK, logsBody{
		Entries:  entries,
		Cursor:   next,
		Missed:   missed,
		Buffered: s.logs.Len(),
	})
}

func parseCursor(raw string) (uint64, error) {
	if raw == "" {
		return 0, nil
	}
	cursor, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, errors.New("since must be a positive whole number")
	}
	return cursor, nil
}

func parseLimit(raw string) (int, error) {
	if raw == "" {
		return defaultLogLimit, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 {
		return 0, errors.New("limit must be a positive whole number")
	}
	return limit, nil
}

// levelRank orders slog's level names. Anything unrecognised is rejected rather
// than silently treated as "everything", which would make a typo look like a
// working filter.
var levelRank = map[string]int{"DEBUG": 0, "INFO": 1, "WARN": 2, "ERROR": 3}

func filterByLevel(entries []logs.Entry, minimum string) ([]logs.Entry, error) {
	floor, ok := levelRank[strings.ToUpper(minimum)]
	if !ok {
		return nil, errors.New("level must be debug, info, warn or error")
	}

	kept := make([]logs.Entry, 0, len(entries))
	for _, entry := range entries {
		if rank, known := levelRank[strings.ToUpper(entry.Level)]; !known || rank >= floor {
			kept = append(kept, entry)
		}
	}
	return kept, nil
}

// filterBySubsystem keeps the entries tagged with a given subsystem.
//
// Unknown values are NOT an error, unlike an unknown level. A level is a closed
// set this package owns, so a typo there is a filter that silently matches
// everything and is worth refusing; a subsystem is whatever a caller passed to
// log.With, so this package has no list to check against and inventing one would
// mean a new subsystem needing a change here before its own lines could be read.
// An unmatched value answers with nothing, which is the truthful result.
func filterBySubsystem(entries []logs.Entry, want string) []logs.Entry {
	kept := make([]logs.Entry, 0, len(entries))
	for _, entry := range entries {
		if strings.EqualFold(entry.Attrs["subsystem"], want) {
			kept = append(kept, entry)
		}
	}
	return kept
}
