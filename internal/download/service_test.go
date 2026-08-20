package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/DulsaraNethmin/curator/internal/indexer"
	"github.com/DulsaraNethmin/curator/internal/store"
	"github.com/DulsaraNethmin/curator/internal/torrent"
)

const testHash = "89599BF4DC369A3A8ECA26411C5CCF922D78B486"

func testMagnet(hash string) string { return "magnet:?xt=urn:btih:" + hash + "&dn=Interstellar" }

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeClient records what it was asked, so a test can assert not just the result
// but the order of the calls that produced it.
type fakeClient struct {
	calls []string

	addErr     error
	byHash     *torrent.Torrent
	byHashErr  error
	torrents   []torrent.Torrent
	listErr    error
	addedMagn  string
	addedCateg string

	// D19's delete path.
	deleted        []string
	deleteErr      error
	deleteCategory string
	deleteFiles    bool
}

func (f *fakeClient) DeleteTorrent(_ context.Context, hash, requireCategory string, deleteFiles bool) error {
	f.calls = append(f.calls, "delete")
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, strings.ToUpper(hash))
	f.deleteCategory, f.deleteFiles = requireCategory, deleteFiles
	return nil
}

func (f *fakeClient) AddMagnet(_ context.Context, magnet, category string) error {
	f.calls = append(f.calls, "add")
	f.addedMagn, f.addedCateg = magnet, category
	return f.addErr
}

func (f *fakeClient) TorrentByHash(_ context.Context, hash string) (*torrent.Torrent, error) {
	f.calls = append(f.calls, "confirm")
	return f.byHash, f.byHashErr
}

func (f *fakeClient) Torrents(_ context.Context, category string) ([]torrent.Torrent, error) {
	f.calls = append(f.calls, "list")
	return f.torrents, f.listErr
}

// fakeStore records every write, so "nothing was written" is a thing a test can
// actually assert rather than infer.
type fakeStore struct {
	calls []string

	movie      store.Movie
	movieErr   error
	wanted     []store.Wanted
	inserted   store.Download
	insertErr  error
	byHash     map[string]store.Download
	getErr     error
	updates    []update
	updateErr  error
	writeCount int

	// D19's delete path.
	deleteErr    error
	deletedMovie bool
}

type update struct {
	hash        string
	state       string
	progress    float64
	reason      string
	completedAt *time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{byHash: map[string]store.Download{}, movie: store.Movie{ID: 7}}
}

func (f *fakeStore) UpsertWanted(_ context.Context, w store.Wanted) (store.Movie, error) {
	f.calls = append(f.calls, "upsert-movie")
	f.writeCount++
	// Recorded so a test can assert what the dispatcher asked for. The real store
	// refuses an empty media type, so a fake that dropped it would let a dispatch
	// that forgot to set one pass here and fail in production.
	f.wanted = append(f.wanted, w)
	return f.movie, f.movieErr
}

func (f *fakeStore) InsertDownload(_ context.Context, d store.Download) (store.Download, error) {
	f.calls = append(f.calls, "insert-download")
	f.writeCount++
	if f.insertErr != nil {
		return store.Download{}, f.insertErr
	}
	f.inserted = d
	d.ID = 1
	return d, nil
}

func (f *fakeStore) UpdateDownloadProgress(_ context.Context, hash, state string, progress float64, reason string, completedAt *time.Time) error {
	f.calls = append(f.calls, "update")
	f.writeCount++
	// Upper-cased exactly as internal/store does, so this fake cannot pass a test
	// the real store would fail — qbit hands out lower-case hashes and the store
	// stores upper-case ones.
	f.updates = append(f.updates, update{strings.ToUpper(hash), state, progress, reason, completedAt})
	return f.updateErr
}

func (f *fakeStore) ListDownloads(context.Context) ([]store.Download, error) {
	f.calls = append(f.calls, "list-downloads")
	out := make([]store.Download, 0, len(f.byHash))
	for _, d := range f.byHash {
		out = append(out, d)
	}
	return out, nil
}

func (f *fakeStore) GetMovie(_ context.Context, id int64) (store.Movie, error) {
	f.calls = append(f.calls, "get-movie")
	if f.movieErr != nil {
		return store.Movie{}, f.movieErr
	}
	return f.movie, nil
}

func (f *fakeStore) DownloadsForMovie(_ context.Context, _ int64) ([]store.Download, error) {
	f.calls = append(f.calls, "downloads-for-movie")
	out := make([]store.Download, 0, len(f.byHash))
	for _, d := range f.byHash {
		out = append(out, d)
	}
	return out, nil
}

func (f *fakeStore) DeleteMovie(_ context.Context, _ int64) (store.Deleted, error) {
	f.calls = append(f.calls, "delete-movie")
	f.writeCount++
	if f.deleteErr != nil {
		return store.Deleted{}, f.deleteErr
	}
	f.deletedMovie = true
	return store.Deleted{Movie: f.movie}, nil
}

func (f *fakeStore) GetDownloadByHash(_ context.Context, hash string) (store.Download, error) {
	f.calls = append(f.calls, "get")
	if f.getErr != nil {
		return store.Download{}, f.getErr
	}
	d, ok := f.byHash[strings.ToUpper(strings.TrimSpace(hash))]
	if !ok {
		return store.Download{}, store.ErrNotFound
	}
	return d, nil
}

// fakeResolver stands in for phase 2's aggregator.
type fakeResolver struct {
	found   indexer.Found
	present bool
	magnet  string
	err     error
}

func (f *fakeResolver) ResolveMagnet(context.Context, string) (string, error) {
	if f.err != nil {
		return "", f.err
	}
	return f.magnet, nil
}

func (f *fakeResolver) Release(string) (indexer.Found, bool) { return f.found, f.present }

func newResolver(magnet string) *fakeResolver {
	return &fakeResolver{
		present: true,
		magnet:  magnet,
		found: indexer.Found{
			Release:  indexer.Release{Title: "Interstellar.2014.1080p.BluRay.x265", Year: 2014},
			ID:       "3f2a9c1b7d4e5a60",
			Indexers: []string{"yts", "tpb"},
		},
	}
}

func newService(c TorrentClient, st Store, r Resolver) *Service {
	return NewService(c, st, r, "curator", quiet())
}

func req() Request {
	return Request{ReleaseID: "3f2a9c1b7d4e5a60", Title: "Interstellar", Year: 2014}
}

func TestDispatchHappyPathInOrder(t *testing.T) {
	client := &fakeClient{byHash: &torrent.Torrent{
		Hash: testHash, Name: "Interstellar", State: torrent.StateDownloading, Progress: 0.1,
	}}
	st := newFakeStore()
	res := newResolver(testMagnet(testHash))

	got, err := newService(client, st, res).Dispatch(context.Background(), req())
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	// The order is the guarantee: add, then confirm, and only then write.
	wantClient := []string{"add", "confirm"}
	if strings.Join(client.calls, ",") != strings.Join(wantClient, ",") {
		t.Errorf("client calls = %v, want %v", client.calls, wantClient)
	}
	wantStore := []string{"get", "upsert-movie", "insert-download"}
	if strings.Join(st.calls, ",") != strings.Join(wantStore, ",") {
		t.Errorf("store calls = %v, want %v", st.calls, wantStore)
	}

	if client.addedCateg != "curator" {
		t.Errorf("category = %q, want curator", client.addedCateg)
	}
	if got.TorrentHash != testHash {
		t.Errorf("hash = %q, want the upper-case %q", got.TorrentHash, testHash)
	}
	if st.inserted.MovieID != 7 {
		t.Errorf("movie_id = %d, want 7", st.inserted.MovieID)
	}
	// Indexer and release name come from the server's own copy, not the caller.
	if st.inserted.Indexer != "yts,tpb" {
		t.Errorf("indexer = %q, want yts,tpb", st.inserted.Indexer)
	}
	if st.inserted.ReleaseName != "Interstellar.2014.1080p.BluRay.x265" {
		t.Errorf("release_name = %q", st.inserted.ReleaseName)
	}
	if st.inserted.State != store.DownloadDownloading {
		t.Errorf("state = %q, want downloading", st.inserted.State)
	}
}

// The test this task exists for: qBittorrent unreachable must write nothing.
func TestDispatchWritesNothingWhenTheClientFails(t *testing.T) {
	client := &fakeClient{addErr: errors.New("dial tcp 127.0.0.1:8080: connection refused")}
	st := newFakeStore()

	_, err := newService(client, st, newResolver(testMagnet(testHash))).Dispatch(context.Background(), req())
	if err == nil {
		t.Fatal("Dispatch succeeded with an unreachable qBittorrent")
	}
	if st.writeCount != 0 {
		t.Errorf("store was written %d times, want 0 — a row here would claim a download nothing has heard of",
			st.writeCount)
	}
}

// An add that "succeeded" but produced no torrent is the 200-Ok.-carrying-a-
// failure case, and it must not be recorded either.
func TestDispatchWritesNothingWhenTheAddIsNotConfirmed(t *testing.T) {
	client := &fakeClient{byHash: nil} // accepted the request, ignored the magnet
	st := newFakeStore()

	_, err := newService(client, st, newResolver(testMagnet(testHash))).Dispatch(context.Background(), req())
	if err == nil {
		t.Fatal("Dispatch succeeded though the torrent was never there")
	}
	if !strings.Contains(err.Error(), "ignored") {
		t.Errorf("err = %v, want it to say the magnet was ignored", err)
	}
	if st.writeCount != 0 {
		t.Errorf("store was written %d times, want 0", st.writeCount)
	}
}

func TestDispatchExpiredReleaseIsTheSentinel(t *testing.T) {
	client := &fakeClient{}
	st := newFakeStore()
	res := &fakeResolver{present: false}

	_, err := newService(client, st, res).Dispatch(context.Background(), req())
	if !errors.Is(err, indexer.ErrReleaseExpired) {
		t.Errorf("err = %v, want ErrReleaseExpired so the API can answer 410", err)
	}
	if len(client.calls) != 0 || st.writeCount != 0 {
		t.Errorf("an expired release touched qBittorrent (%v) or the store (%d writes)", client.calls, st.writeCount)
	}
}

func TestDispatchMalformedMagnetFailsBeforeTheClient(t *testing.T) {
	client := &fakeClient{}
	st := newFakeStore()
	res := newResolver("magnet:?dn=no-info-hash-here")

	_, err := newService(client, st, res).Dispatch(context.Background(), req())
	if err == nil {
		t.Fatal("Dispatch accepted a magnet with no info hash")
	}
	if len(client.calls) != 0 {
		t.Errorf("client was called %v, want nothing — the magnet is unusable", client.calls)
	}
	if st.writeCount != 0 {
		t.Errorf("store written %d times, want 0", st.writeCount)
	}
}

func TestDispatchOfAnAlreadyRunningDownloadReturnsTheExistingRow(t *testing.T) {
	client := &fakeClient{}
	st := newFakeStore()
	st.byHash[testHash] = store.Download{ID: 42, TorrentHash: testHash, State: store.DownloadDownloading}

	got, err := newService(client, st, newResolver(testMagnet(testHash))).Dispatch(context.Background(), req())
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if got.ID != 42 {
		t.Errorf("id = %d, want the existing row's 42", got.ID)
	}
	if len(client.calls) != 0 {
		t.Errorf("client was called %v, want nothing — it is already downloading", client.calls)
	}
	if st.writeCount != 0 {
		t.Errorf("store written %d times, want 0", st.writeCount)
	}
}

func TestDispatchRequiresATitle(t *testing.T) {
	st := newFakeStore()
	_, err := newService(&fakeClient{}, st, newResolver(testMagnet(testHash))).
		Dispatch(context.Background(), Request{ReleaseID: "x", Title: "   "})
	if err == nil {
		t.Fatal("Dispatch accepted a blank title")
	}
	if st.writeCount != 0 {
		t.Errorf("store written %d times, want 0", st.writeCount)
	}
}

// An unconfigured qBittorrent is a configuration state, not a crash.
func TestDispatchUnconfigured(t *testing.T) {
	_, err := NewService(nil, newFakeStore(), newResolver(testMagnet(testHash)), "curator", quiet()).
		Dispatch(context.Background(), req())
	if !errors.Is(err, ErrUnconfigured) {
		t.Errorf("err = %v, want ErrUnconfigured", err)
	}
}

// --- resume -----------------------------------------------------------------

// resumeClient answers TorrentByHash per hash, so a test can say which torrents
// the client still holds and which it has forgotten.
type resumeClient struct {
	fakeClient
	held  map[string]bool
	added []string
}

func (r *resumeClient) TorrentByHash(_ context.Context, hash string) (*torrent.Torrent, error) {
	if r.byHashErr != nil {
		return nil, r.byHashErr
	}
	if !r.held[strings.ToUpper(hash)] {
		return nil, nil
	}
	return &torrent.Torrent{Hash: strings.ToUpper(hash), State: torrent.StateDownloading}, nil
}

func (r *resumeClient) AddMagnet(_ context.Context, magnet, _ string) error {
	if r.addErr != nil {
		return r.addErr
	}
	r.added = append(r.added, magnet)
	return nil
}

func resumeStore(rows ...store.Download) *fakeStore {
	st := newFakeStore()
	for _, row := range rows {
		st.byHash[row.TorrentHash] = row
	}
	return st
}

func row(hash, state, magnet string) store.Download {
	return store.Download{TorrentHash: hash, State: state, Magnet: magnet, ReleaseName: "Interstellar." + state}
}

// TestResumeReAddsWhatIsNotImported is the whole of boot resume: every row that
// has not become a film goes back to the client, and the ones that have do not.
func TestResumeReAddsWhatIsNotImported(t *testing.T) {
	st := resumeStore(
		row("AAAA000000000000000000000000000000000000", store.DownloadDownloading, "magnet:?xt=urn:btih:aaaa"),
		row("BBBB000000000000000000000000000000000000", store.DownloadStalled, "magnet:?xt=urn:btih:bbbb"),
		row("CCCC000000000000000000000000000000000000", store.DownloadCompleted, "magnet:?xt=urn:btih:cccc"),
		row("DDDD000000000000000000000000000000000000", store.DownloadImported, "magnet:?xt=urn:btih:dddd"),
	)
	client := &resumeClient{held: map[string]bool{}}

	if err := newService(client, st, newResolver("")).Resume(context.Background()); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	sort.Strings(client.added)
	want := []string{"magnet:?xt=urn:btih:aaaa", "magnet:?xt=urn:btih:bbbb", "magnet:?xt=urn:btih:cccc"}
	if strings.Join(client.added, ",") != strings.Join(want, ",") {
		t.Errorf("re-added %v, want %v — imported rows are films, not downloads", client.added, want)
	}
}

// A client that still holds the torrent is not sent a duplicate: qBittorrent
// answers "Fails." to a magnet it already has, and every boot would log an
// error for a torrent that is perfectly healthy.
func TestResumeSkipsWhatTheClientStillHolds(t *testing.T) {
	st := resumeStore(
		row("AAAA000000000000000000000000000000000000", store.DownloadDownloading, "magnet:?xt=urn:btih:aaaa"),
	)
	client := &resumeClient{held: map[string]bool{"AAAA000000000000000000000000000000000000": true}}

	if err := newService(client, st, newResolver("")).Resume(context.Background()); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if len(client.added) != 0 {
		t.Errorf("re-added %v, want nothing — the client already has it", client.added)
	}
}

// One bad row must not cost the others. This is a boot path: whatever it skips,
// nothing else will pick up until the next restart.
func TestResumeCarriesOnPastAFailure(t *testing.T) {
	st := resumeStore(
		row("AAAA000000000000000000000000000000000000", store.DownloadDownloading, ""),
		row("BBBB000000000000000000000000000000000000", store.DownloadDownloading, "magnet:?xt=urn:btih:bbbb"),
	)
	client := &resumeClient{held: map[string]bool{}}

	if err := newService(client, st, newResolver("")).Resume(context.Background()); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if len(client.added) != 1 || client.added[0] != "magnet:?xt=urn:btih:bbbb" {
		t.Errorf("re-added %v, want only the row that has a magnet", client.added)
	}
}

// With no client configured there is nothing to resume into, and that is a
// supported state rather than an error — the same posture an unset QBIT_USER
// has had since phase 3.
func TestResumeWithoutAClientIsNotAnError(t *testing.T) {
	st := resumeStore(row("AAAA000000000000000000000000000000000000", store.DownloadDownloading, "magnet:?x"))
	if err := NewService(nil, st, newResolver(""), "curator", quiet()).Resume(context.Background()); err != nil {
		t.Fatalf("Resume with no client: %v", err)
	}
}

// --- the dispatch guard (T37) ------------------------------------------------

// TestDispatchRefusedByTheGuard: a refused dispatch must cost nothing and leave
// nothing behind — no resolve, no add, no row. The same discipline the expired
// release check already has.
func TestDispatchRefusedByTheGuard(t *testing.T) {
	client := &fakeClient{}
	st := newFakeStore()
	res := newResolver(testMagnet(testHash))

	// A plain error, as internal/vpn returns: the class is this package's to
	// add, so that a guard which cannot answer is refused the same way as one
	// that answers no.
	svc := newService(client, st, res).WithGuard(func(context.Context) error {
		return errors.New("the tunnel is down")
	})

	_, err := svc.Dispatch(context.Background(), req())
	if !errors.Is(err, ErrUnprotected) {
		t.Fatalf("err = %v, want it to wrap ErrUnprotected so the API answers 503", err)
	}
	if !strings.Contains(err.Error(), "the tunnel is down") {
		t.Errorf("err = %q, want the guard's own reason to survive", err)
	}
	if len(client.calls) != 0 {
		t.Errorf("the torrent client was called %v for a refused dispatch", client.calls)
	}
	if st.writeCount != 0 {
		t.Errorf("%d writes for a refused dispatch, want none", st.writeCount)
	}
}

// A guard that passes is invisible: the dispatch behaves exactly as it did
// before there was one.
func TestDispatchProceedsWhenTheGuardPasses(t *testing.T) {
	client := &fakeClient{byHash: &torrent.Torrent{
		Hash: testHash, Name: "Interstellar", State: torrent.StateDownloading, Progress: 0.1,
	}}
	var asked int
	svc := newService(client, newFakeStore(), newResolver(testMagnet(testHash))).
		WithGuard(func(context.Context) error { asked++; return nil })

	if _, err := svc.Dispatch(context.Background(), req()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if asked != 1 {
		t.Errorf("the guard was asked %d times, want exactly 1", asked)
	}
}

// NotCompleted must answer errors.Is(err, ErrNotCompleted) through Import's own
// wrap, or the API answers 500 for a download that has simply not finished.
func TestNotCompletedIsStillTheSentinel(t *testing.T) {
	err := fmt.Errorf("import ABC: %w", NotCompleted{State: store.DownloadStalled})
	if !errors.Is(err, ErrNotCompleted) {
		t.Fatal("errors.Is lost the sentinel, and the API answers 500 instead of 409")
	}

	var pending NotCompleted
	if !errors.As(err, &pending) || pending.State != store.DownloadStalled {
		t.Errorf("errors.As recovered %+v, want the state back", pending)
	}

	// "stalled" and "downloading" are different things to be told to wait for,
	// which is the whole reason the state is carried rather than dropped.
	if !strings.Contains(err.Error(), store.DownloadStalled) {
		t.Errorf("the state is not in the message: %q", err)
	}
	if strings.Contains(NotCompleted{}.Error(), ErrNotCompleted.Error()) {
		t.Error("Error() restates the sentinel it unwraps to")
	}
}

// Unprotected must answer errors.Is(err, ErrUnprotected) through Dispatch's own
// wrap, or a refused dispatch answers 500 instead of 503 — a clean build, a
// wrong status, and nothing to say. It must ALSO keep answering for whatever the
// guard failed with, because the wrap it replaces was `%w: %w` and a guard that
// timed out is a different operational fact from one that refused.
func TestUnprotectedIsStillTheSentinel(t *testing.T) {
	guard := fmt.Errorf("could not ask the torrent client where it appears from: %w", context.DeadlineExceeded)
	err := fmt.Errorf("dispatch yts-1: %w", UnprotectedFor(guard))

	if !errors.Is(err, ErrUnprotected) {
		t.Fatal("errors.Is lost the sentinel, and the API answers 500 instead of 503")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Error("errors.Is lost the guard's own cause, which the `%w: %w` wrap used to keep")
	}

	var unprotected Unprotected
	if !errors.As(err, &unprotected) {
		t.Fatal("errors.As cannot recover the reason through the wrap")
	}
	if unprotected.Reason != guard.Error() {
		t.Errorf("reason = %q, want the guard's own sentence back", unprotected.Reason)
	}

	// ErrUnprotected's text is reached through Unwrap and must not be restated:
	// the two together are what read as one refusal said twice.
	if strings.Contains(unprotected.Error(), ErrUnprotected.Error()) {
		t.Errorf("Error() restates the sentinel it unwraps to: %q", unprotected.Error())
	}

	// A zero value is what an API test constructs when it only needs the class,
	// and it must not put a nil in the Unwrap slice.
	if !errors.Is(Unprotected{Reason: "x"}, ErrUnprotected) {
		t.Error("a zero-cause Unprotected lost the sentinel")
	}
}

// TestResumeAtBootAsksTheGuard is the one path in this process that could start
// unprotected traffic with nobody in front of it.
//
// Dispatch is somebody choosing to start a download. This is the whole library
// restarting itself on a box that has just rebooted — which is exactly when a
// tunnel is most likely to be down and least likely to be watched. It re-added
// every unfinished magnet without asking.
func TestResumeAtBootAsksTheGuard(t *testing.T) {
	st := resumeStore(
		row("AAAA000000000000000000000000000000000000", store.DownloadDownloading, "magnet:?xt=urn:btih:aaaa"),
		row("BBBB000000000000000000000000000000000000", store.DownloadStalled, "magnet:?xt=urn:btih:bbbb"),
	)
	client := &resumeClient{held: map[string]bool{}}

	refused := errors.New("the tunnel's exit address is the host's own")
	svc := newService(client, st, newResolver("")).
		WithGuard(func(context.Context) error { return refused })

	// Not an error: the tunnel coming back is an expected event, not a startup
	// failure, and the library, search and the UI have nothing to do with it.
	if err := svc.Resume(context.Background()); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if len(client.added) != 0 {
		t.Errorf("re-added %v with the guard refusing; a reboot into a broken tunnel would have "+
			"restarted the whole library unprotected", client.added)
	}
}

// And the second chance the refusal is written to expect. cmd/curator calls
// Resume again when the sentinel's verdict turns good, which only works because
// nothing was lost and the loop skips what the client already holds.
func TestResumeRunsAgainOnceTheGuardPasses(t *testing.T) {
	st := resumeStore(
		row("AAAA000000000000000000000000000000000000", store.DownloadDownloading, "magnet:?xt=urn:btih:aaaa"),
	)
	client := &resumeClient{held: map[string]bool{}}

	var protected bool
	svc := newService(client, st, newResolver("")).
		WithGuard(func(context.Context) error {
			if protected {
				return nil
			}
			return errors.New("not yet")
		})

	if err := svc.Resume(context.Background()); err != nil {
		t.Fatalf("first Resume: %v", err)
	}
	if len(client.added) != 0 {
		t.Fatalf("re-added %v before the tunnel proved out", client.added)
	}

	protected = true
	if err := svc.Resume(context.Background()); err != nil {
		t.Fatalf("second Resume: %v", err)
	}
	if len(client.added) != 1 {
		t.Errorf("re-added %v once the guard passed, want the one unfinished row", client.added)
	}

	// Idempotent, because the sentinel may pass more than once.
	client.held["AAAA000000000000000000000000000000000000"] = true
	if err := svc.Resume(context.Background()); err != nil {
		t.Fatalf("third Resume: %v", err)
	}
	if len(client.added) != 1 {
		t.Errorf("re-added %v after a second good verdict; the client already holds it", client.added)
	}
}
