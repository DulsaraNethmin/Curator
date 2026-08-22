package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DulsaraNethmin/curator/internal/store"
	"github.com/DulsaraNethmin/curator/internal/tmdb"
)

// sseEvent is one record off the wire.
type sseEvent struct {
	name string
	data []byte
}

// sseReader parses the subset of SSE curator emits, and no more: one `event:`
// line, one `data:` line, a blank line. It is deliberately strict — an unknown
// line fails the test rather than being skipped — because the value of a test
// parser here is that it notices the day the server starts emitting something
// the browser's equally small parser would not understand.
type sseReader struct{ br *bufio.Reader }

func newSSEReader(r io.Reader) *sseReader { return &sseReader{br: bufio.NewReader(r)} }

// next returns the following record, or false at the end of the stream. It
// blocks until one arrives, which is what makes the ordering tests below
// deterministic: they wait for events rather than sleeping and hoping.
func (s *sseReader) next(t *testing.T) (sseEvent, bool) {
	t.Helper()
	var event sseEvent
	for {
		line, err := s.br.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) && strings.TrimSpace(line) == "" {
				return sseEvent{}, false
			}
			t.Fatalf("reading the stream: %v (partial line %q)", err, line)
		}
		switch line = strings.TrimSuffix(line, "\n"); {
		case line == "":
			if event.name != "" {
				return event, true
			}
		case strings.HasPrefix(line, "event: "):
			event.name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			event.data = []byte(strings.TrimPrefix(line, "data: "))
		default:
			t.Fatalf("the stream sent a line neither end knows how to read: %q", line)
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("encode %T: %v", v, err)
	}
	return out
}

func (e sseEvent) row(t *testing.T) discoverRow {
	t.Helper()
	var row discoverRow
	if err := json.Unmarshal(e.data, &row); err != nil {
		t.Fatalf("decode a %s event: %v — data was %s", e.name, err, e.data)
	}
	return row
}

// streamEvents runs the whole stream into memory. Fine for everything except
// the two tests about WHEN a rail arrives, which need a real connection.
func streamEvents(t *testing.T, h http.Handler, target string) (*httptest.ResponseRecorder, []sseEvent) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	if rec.Code != http.StatusOK {
		return rec, nil
	}
	var events []sseEvent
	reader := newSSEReader(rec.Body)
	for {
		event, ok := reader.next(t)
		if !ok {
			return rec, events
		}
		events = append(events, event)
	}
}

// oneCardPerRail is the fake the agreement and ordering tests share: every rail
// answers with a card nothing else returns, so a row arriving under the wrong id
// is visible. Same shape as TestEachDiscoverRailDrawsItsOwnSource's, for the same
// reason — counts and envelopes stay right under a transposition, contents do not.
//
// It fills a fake in place rather than returning one, so the counting variant can
// share it. fakeBrowser holds a mutex, and a helper that returned one by value
// would be a copied lock and a go vet failure.
func fillOneCardPerRail(browser *fakeBrowser) *fakeBrowser {
	card := func(id int, title string) []tmdb.Match {
		return []tmdb.Match{{TMDBID: id, Title: title, Year: 2024}}
	}
	browser.trending = card(1, "a trending film")
	browser.popular = card(2, "a popular film")
	browser.topRated = card(3, "a top rated film")
	browser.nowPlaying = card(4, "a film in cinemas")
	browser.byGenre = map[int][]tmdb.Match{}
	for i, genre := range movieGenres {
		browser.byGenre[genre.id] = card(100+i, genre.title+" film")
	}
	return browser
}

func oneCardPerRail() *fakeBrowser { return fillOneCardPerRail(&fakeBrowser{}) }

// movieRails is the rail table this package draws films from. Tests assert
// against it rather than against a written-down twelve, so adding a genre does
// not fail a test that is not about genres.
func movieRails() []discoverRail {
	return (&Server{browser: &fakeBrowser{}}).discoverRails(store.MediaTypeMovie)
}

// The stream opens by naming every rail, in the order the page draws them,
// before a single one has answered.
//
// That event is the whole reason this route can be laid out at once: the page
// gets twelve real headings and twelve correctly-sized skeletons immediately and
// never reflows as the rails land. Without it a streamed screen would grow
// downward one rail at a time, which is a worse arrival than the one T103's
// skeletons were built to remove.
func TestTheStreamNamesEveryRailBeforeAnyOfThemAnswer(t *testing.T) {
	rec, events := streamEvents(t, browseServer(t, oneCardPerRail(), nil), "/api/tmdb/discover/stream")

	if got := rec.Header().Get("content-type"); got != "text/event-stream" {
		t.Errorf("content-type = %q, want text/event-stream", got)
	}
	// The header proxies read to stop buffering. Without it nginx holds every
	// rail until the last one and this route silently becomes the one it replaces.
	if got := rec.Header().Get("x-accel-buffering"); got != "no" {
		t.Errorf("x-accel-buffering = %q, want no", got)
	}
	if len(events) == 0 {
		t.Fatal("the stream sent nothing")
	}
	if events[0].name != "rails" {
		t.Fatalf("the first event is %q, want rails", events[0].name)
	}

	var opening struct {
		Media string     `json:"media"`
		Rails []railSlot `json:"rails"`
	}
	if err := json.Unmarshal(events[0].data, &opening); err != nil {
		t.Fatalf("decode the opening event: %v — data was %s", err, events[0].data)
	}
	if opening.Media != store.MediaTypeMovie {
		t.Errorf("media = %q, want %q", opening.Media, store.MediaTypeMovie)
	}

	// Against the rail table itself, in order.
	want := movieRails()
	if len(opening.Rails) != len(want) {
		t.Fatalf("the opening event names %d rails, want %d", len(opening.Rails), len(want))
	}
	for i := range want {
		if opening.Rails[i].ID != want[i].id || opening.Rails[i].Title != want[i].title {
			t.Errorf("rail %d = %+v, want {%s %s}",
				i, opening.Rails[i], want[i].id, want[i].title)
		}
	}
}

// The claim this route exists to make: a rail that has answered is sent while a
// slower one is still in flight.
//
// It needs a real connection. httptest.ResponseRecorder buffers, so a test
// against it cannot tell a stream from a response that happened to be written in
// pieces — which is exactly the failure the x-accel-buffering header is about,
// and asserting it in a buffer would be asserting nothing.
//
// There is no sleep here. The reader blocks for eleven rows, and the twelfth
// cannot arrive until the test releases it, so the ordering is a fact rather
// than a race that usually goes the right way.
func TestARailIsSentWhileASlowerOneIsStillInFlight(t *testing.T) {
	release := make(chan struct{})
	browser := &slowRail{fakeBrowser: oneCardPerRail(), release: release}

	server := httptest.NewServer(browseServer(t, browser, nil))
	defer server.Close()

	resp, err := server.Client().Get(server.URL + "/api/tmdb/discover/stream")
	if err != nil {
		t.Fatalf("GET the stream: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	reader := newSSEReader(resp.Body)
	opening, ok := reader.next(t)
	if !ok || opening.name != "rails" {
		t.Fatalf("first event = %+v, want rails", opening)
	}

	// Eleven of twelve, read before the twelfth is allowed to exist.
	rails := movieRails()
	early := map[string]bool{}
	for range len(rails) - 1 {
		event, ok := reader.next(t)
		if !ok {
			t.Fatalf("the stream ended after %d rows, with one rail still blocked", len(early))
		}
		if event.name != "row" {
			t.Fatalf("event %q arrived before the blocked rail finished", event.name)
		}
		early[event.row(t).ID] = true
	}
	if early["popular"] {
		t.Fatal("the blocked rail arrived: the fake did not block, and this test proves nothing")
	}
	if len(early) != len(rails)-1 {
		t.Fatalf("got %d distinct rails early, want %d — a rail was sent twice", len(early), len(rails)-1)
	}

	close(release)

	last, ok := reader.next(t)
	if !ok {
		t.Fatal("the released rail never arrived")
	}
	if last.name != "row" || last.row(t).ID != "popular" {
		t.Fatalf("after the release: %s %s, want the popular row", last.name, last.data)
	}
	if end, ok := reader.next(t); !ok || end.name != "done" {
		t.Fatalf("the stream ended with %+v, want done", end)
	}
}

// slowRail is oneCardPerRail with its Popular held until a test lets go.
//
// Embedded rather than a new field on fakeBrowser: blocking is a property of
// this one test, and a channel on the shared fake would be a nil receive waiting
// for every other test in the package to forget to close it.
type slowRail struct {
	*fakeBrowser
	release <-chan struct{}
}

func (b *slowRail) Popular(ctx context.Context) ([]tmdb.Match, error) {
	select {
	case <-b.release:
		return b.fakeBrowser.Popular(ctx)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// The two routes draw the same screen, and this is the test that keeps them
// doing it.
//
// Two routes over one table is a shape that rots: a rail added to one, a field
// renamed in the other, and the streamed home page quietly stops matching the
// one `curl | jq` shows. Both are asked here from identical fakes and every row
// is compared field for field.
func TestTheStreamAndTheOneResponseAgreeRailForRail(t *testing.T) {
	// Two servers and two fakes, not one of each: a single server would serve the
	// second route from the rail cache the first one filled, and a stream that
	// only ever agreed about cached rails is not the thing being checked.
	path := "/media/movies/Avengers - Endgame (2019)"
	library := func() *fakeStore {
		st := newFakeStore()
		st.library = map[int64]store.LibraryState{
			1: {MovieID: 7, Status: store.StatusImported, LibraryPath: &path},
		}
		return st
	}

	var buffered struct {
		Media string        `json:"media"`
		Rows  []discoverRow `json:"rows"`
	}
	getJSON(t, browseServer(t, oneCardPerRail(), library()), "/api/tmdb/discover", &buffered)

	_, events := streamEvents(t,
		browseServer(t, oneCardPerRail(), library()), "/api/tmdb/discover/stream")

	streamed := map[string]discoverRow{}
	for _, event := range events {
		if event.name == "row" {
			row := event.row(t)
			streamed[row.ID] = row
		}
	}

	if len(streamed) != len(buffered.Rows) {
		t.Fatalf("the stream sent %d rows, the response carried %d", len(streamed), len(buffered.Rows))
	}
	for _, want := range buffered.Rows {
		got, ok := streamed[want.ID]
		if !ok {
			t.Errorf("the stream never sent %q", want.ID)
			continue
		}
		// Compared as JSON, which is the only thing either route promises. %+v
		// prints a *libraryStateBody as its address, so two identical badges
		// never match and two absent ones always do — the comparison would fail
		// on every card that HAS a badge and pass on every card that does not,
		// which is precisely backwards.
		if string(mustJSON(t, got)) != string(mustJSON(t, want)) {
			t.Errorf("rail %q:\n  stream   %s\n  response %s",
				want.ID, mustJSON(t, got), mustJSON(t, want))
		}
	}

	// The badge specifically, because it is the half that is NOT cached (T102)
	// and the half a second route is most likely to forget to merge.
	if row := streamed["trending"]; len(row.Results) != 1 || row.Results[0].Library == nil {
		t.Errorf("the streamed trending card lost its library badge: %+v", row.Results)
	}
}

// A rail that failed is an event like any other, not the end of the stream.
//
// The failure envelope is the reason: one rail down is a success carrying the
// other eleven (D41's shape), and a stream that aborted on the first refused
// request would turn every rail after it into a screen that never finishes
// loading, with nothing to say why.
func TestAFailingRailIsAnEventAndNotAStreamError(t *testing.T) {
	browser := oneCardPerRail()
	browser.popularErr = errors.New("dial tcp: connection refused")

	rec, events := streamEvents(t, browseServer(t, browser, nil), "/api/tmdb/discover/stream")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a downed rail is not an error page", rec.Code)
	}

	rows := map[string]discoverRow{}
	var names []string
	for _, event := range events {
		names = append(names, event.name)
		if event.name == "row" {
			row := event.row(t)
			rows[row.ID] = row
		}
	}
	if len(rows) != 4+len(movieGenres) {
		t.Fatalf("got %d rows, want %d — events were %v", len(rows), 4+len(movieGenres), names)
	}
	if failed := rows["popular"]; failed.OK || failed.Error == "" || failed.Results == nil {
		t.Errorf("the failed rail = %+v, want ok:false, a reason, and [] rather than null", failed)
	}
	if ok := rows["trending"]; !ok.OK || len(ok.Results) != 1 {
		t.Errorf("a healthy rail was lost with the failing one: %+v", ok)
	}
	if names[len(names)-1] != "done" {
		t.Errorf("the stream ended with %q, want done", names[len(names)-1])
	}
}

// Every refusal this screen has happens before the stream opens, so it is still
// a status code with a sentence in it.
//
// That is what beginDiscover being in front of the first write buys, and it is
// worth pinning: the moment a gate moves after it, its 503 becomes a 200 that
// stops after one event, and the browser's own error path — which reads a JSON
// body — never sees it.
func TestTheStreamRefusesBeforeItOpens(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler func(*testing.T) http.Handler
		target  string
		status  int
		says    string
	}{
		{
			name:    "no tmdb key",
			handler: func(t *testing.T) http.Handler { return browseServer(t, nil, nil) },
			target:  "/api/tmdb/discover/stream",
			status:  http.StatusServiceUnavailable,
			says:    "TMDB_API_KEY",
		},
		{
			name:    "television is off",
			handler: func(t *testing.T) http.Handler { return browseServer(t, oneCardPerRail(), nil) },
			target:  "/api/tmdb/discover/stream?media=tv",
			status:  http.StatusServiceUnavailable,
			says:    "LIBRARY_TV",
		},
		{
			name:    "not a media type",
			handler: func(t *testing.T) http.Handler { return browseServer(t, oneCardPerRail(), nil) },
			target:  "/api/tmdb/discover/stream?media=tvshow",
			status:  http.StatusBadRequest,
			says:    "media",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.handler(t).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.target, nil))

			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d — body %s", rec.Code, tc.status, rec.Body)
			}
			if got := rec.Header().Get("content-type"); !strings.HasPrefix(got, "application/json") {
				t.Errorf("content-type = %q, want json: a refusal is not a stream", got)
			}
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode the refusal: %v — body was %s", err, rec.Body)
			}
			if !strings.Contains(body.Error, tc.says) {
				t.Errorf("the refusal is %q, want it to name %s", body.Error, tc.says)
			}
		})
	}
}

// One cache behind both routes, and the stream fills it.
//
// Two caches would be the same screen asking TMDB twice as often for no reason
// anybody could see from either route, and it is the kind of thing a second
// implementation acquires by accident — a cache reached through a field the new
// handler forgot to use reads as merely slow rather than as broken.
func TestAStreamedRailFillsTheCacheTheOtherRouteReads(t *testing.T) {
	browser := &countingBrowser{}
	fillOneCardPerRail(&browser.fakeBrowser)
	handler := browseServer(t, browser, nil)

	streamEvents(t, handler, "/api/tmdb/discover/stream")
	if got := browser.trendingCalls(); got != 1 {
		t.Fatalf("the stream asked trending %d times, want once", got)
	}

	var body struct {
		Rows []discoverRow `json:"rows"`
	}
	getJSON(t, handler, "/api/tmdb/discover", &body)
	if got := browser.trendingCalls(); got != 1 {
		t.Errorf("the second route re-asked trending: %d calls, want the stream's cache", got)
	}

	byID := map[string]discoverRow{}
	for _, row := range body.Rows {
		byID[row.ID] = row
	}
	if len(byID["trending"].Results) != 1 {
		t.Errorf("the cached rail came back empty: %+v", byID["trending"])
	}
}

// Television streams from the television endpoints, under television's titles.
//
// The same contamination check D48 asks of every media-scoped path: the fake
// answers the show rails from their own fields, so a stream that forgot to
// switch would arrive with films in it.
func TestTheStreamAsksTelevisionsOwnRails(t *testing.T) {
	browser := oneCardPerRail()
	browser.showTrending = []tmdb.Match{severance()}
	browser.showByGenre = map[int][]tmdb.Match{}

	_, events := streamEvents(t, tvBrowseServer(t, browser), "/api/tmdb/discover/stream?media=tv")

	var opening struct {
		Media string     `json:"media"`
		Rails []railSlot `json:"rails"`
	}
	if len(events) == 0 {
		t.Fatal("the stream sent nothing")
	}
	if err := json.Unmarshal(events[0].data, &opening); err != nil {
		t.Fatalf("decode the opening event: %v", err)
	}
	if opening.Media != store.MediaTypeTV {
		t.Errorf("media = %q, want %q", opening.Media, store.MediaTypeTV)
	}
	// in_release is the one rail whose title differs between the two, which
	// makes it the one that proves the television table was the one walked.
	for _, slot := range opening.Rails {
		if slot.ID == "in_release" && slot.Title != "On the air this week" {
			t.Errorf("in_release is titled %q on the television stream", slot.Title)
		}
	}

	for _, event := range events {
		if event.name != "row" {
			continue
		}
		if row := event.row(t); row.ID == "trending" {
			if len(row.Results) != 1 || row.Results[0].TMDBID != severance().TMDBID {
				t.Errorf("the television trending rail carried %+v, want Severance", row.Results)
			}
		}
	}
}
