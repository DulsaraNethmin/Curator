// Package logs keeps the tail of the process's own log in memory, so the UI can
// show what curator is doing without anyone having to SSH into the Pi and read
// journalctl.
//
// It is a ring buffer behind an slog.Handler that tees: every record still goes
// to stderr exactly as before, and a copy is kept here. Nothing about the
// existing logging changes, and no call site has to know this exists.
//
// The redaction is not decoration. Until now a log line went to stderr on a
// machine you had to SSH into; behind GET /api/logs it goes to any browser on
// the LAN, and there is no authentication in front of it (docs/decisions.md
// D17, D18). internal/tmdb already scrubs the API key out of transport errors
// at the source — this is the second line, so that the guarantee holds for log
// calls nobody has written yet rather than only for the careful ones.
package logs

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// DefaultSize is how many entries are kept. A thousand lines is a few hundred
// kilobytes and covers hours of a quiet poller or the whole of a busy import.
const DefaultSize = 1000

// Redacted is what a secret is replaced with. It is deliberately not a run of
// asterisks matching the length: that would still disclose how long the secret
// is.
const Redacted = "[redacted]"

// Entry is one log record, as the API serves it.
//
// Seq is a monotonic cursor from 1, so the UI can ask for "everything after
// what I already have" instead of re-fetching the whole tail every poll.
type Entry struct {
	Seq   uint64            `json:"seq"`
	Time  time.Time         `json:"time"`
	Level string            `json:"level"`
	Msg   string            `json:"msg"`
	Attrs map[string]string `json:"attrs,omitempty"`
}

// Buffer holds the last N entries. It is safe for concurrent use: the poller,
// every HTTP handler and the importer all log from different goroutines.
type Buffer struct {
	mu    sync.RWMutex
	ring  []Entry
	total uint64 // entries ever recorded; also the Seq of the newest

	// secrets are scrubbed out of every message and attribute. Empty values are
	// dropped at construction — an unset QBIT_PASS is "", and redacting the
	// empty string would rewrite every line into nonsense.
	secrets []string
}

// NewBuffer returns a buffer of size entries which scrubs the given secrets out
// of everything it records.
func NewBuffer(size int, secrets ...string) *Buffer {
	if size <= 0 {
		size = DefaultSize
	}
	kept := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		// Short values are refused as well as empty ones: redacting a two-character
		// "password" would mangle unrelated words and make the log unreadable
		// while protecting nothing worth protecting.
		if len(strings.TrimSpace(secret)) >= 6 {
			kept = append(kept, secret)
		}
	}
	return &Buffer{ring: make([]Entry, size), secrets: kept}
}

// Handler wraps next so records go to both. Pass the handler you would
// otherwise have given slog.New.
func (b *Buffer) Handler(next slog.Handler) slog.Handler {
	return &teeHandler{inner: next, buf: b}
}

// Since returns the entries after cursor, newest last, along with the new
// cursor and how many entries were missed.
//
// missed is not cosmetic: a client that polls slowly, or one that was closed
// over a busy import, will have had entries fall off the ring. Saying so lets
// the UI print "N lines dropped" instead of silently showing a discontinuous
// log, which is the kind of thing that costs somebody an hour.
func (b *Buffer) Since(cursor uint64, limit int) (entries []Entry, next uint64, missed uint64) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	size := uint64(len(b.ring))
	if b.total == 0 {
		return nil, 0, 0
	}
	if limit <= 0 || uint64(limit) > size {
		limit = int(size)
	}

	oldest := uint64(1)
	if b.total > size {
		oldest = b.total - size + 1
	}

	start := cursor + 1
	if start < oldest {
		missed = oldest - start
		start = oldest
	}
	if start > b.total {
		return nil, b.total, 0 // caller is up to date
	}

	// Too far behind to serve in one response: hand back the newest `limit` and
	// count the rest as missed, rather than dribbling out ancient history while
	// the live tail runs away.
	if available := b.total - start + 1; available > uint64(limit) {
		missed += available - uint64(limit)
		start = b.total - uint64(limit) + 1
	}

	entries = make([]Entry, 0, b.total-start+1)
	for seq := start; seq <= b.total; seq++ {
		entries = append(entries, b.ring[(seq-1)%size])
	}
	return entries, b.total, missed
}

// Len reports how many entries are currently held.
func (b *Buffer) Len() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.total < uint64(len(b.ring)) {
		return int(b.total)
	}
	return len(b.ring)
}

// record stores one entry, scrubbing secrets first.
func (b *Buffer) record(entry Entry) {
	entry.Msg = b.scrub(entry.Msg)
	for key, value := range entry.Attrs {
		entry.Attrs[key] = b.scrub(value)
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	b.total++
	entry.Seq = b.total
	b.ring[(b.total-1)%uint64(len(b.ring))] = entry
}

func (b *Buffer) scrub(s string) string {
	for _, secret := range b.secrets {
		if strings.Contains(s, secret) {
			s = strings.ReplaceAll(s, secret, Redacted)
		}
	}
	return s
}

// teeHandler writes every record to the wrapped handler and keeps a copy.
//
// It holds its own attrs and group prefix because slog.Handler.WithAttrs and
// WithGroup return a NEW handler: a logger built with .With(...) would
// otherwise lose those attributes on the copy kept here, and the buffered line
// would say less than the one on stderr.
type teeHandler struct {
	inner slog.Handler
	buf   *Buffer
	attrs []slog.Attr
	group string
}

func (h *teeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *teeHandler) Handle(ctx context.Context, record slog.Record) error {
	entry := Entry{
		Time:  record.Time,
		Level: record.Level.String(),
		Msg:   record.Message,
		Attrs: make(map[string]string, record.NumAttrs()+len(h.attrs)),
	}
	for _, attr := range h.attrs {
		entry.Attrs[h.key(attr.Key)] = attr.Value.String()
	}
	record.Attrs(func(attr slog.Attr) bool {
		entry.Attrs[h.key(attr.Key)] = attr.Value.String()
		return true
	})
	if len(entry.Attrs) == 0 {
		entry.Attrs = nil
	}
	h.buf.record(entry)

	// stderr still gets everything, unchanged. The buffer is additive: if this
	// package were deleted, the logs would be exactly what they were before.
	return h.inner.Handle(ctx, record)
}

func (h *teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	merged = append(merged, h.attrs...)
	merged = append(merged, attrs...)
	return &teeHandler{inner: h.inner.WithAttrs(attrs), buf: h.buf, attrs: merged, group: h.group}
}

func (h *teeHandler) WithGroup(name string) slog.Handler {
	group := name
	if h.group != "" {
		group = h.group + "." + name
	}
	return &teeHandler{inner: h.inner.WithGroup(name), buf: h.buf, attrs: h.attrs, group: group}
}

func (h *teeHandler) key(key string) string {
	if h.group == "" {
		return key
	}
	return h.group + "." + key
}
