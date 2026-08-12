package download

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/DulsaraNethmin/curator/internal/indexer"
	"github.com/DulsaraNethmin/curator/internal/qbit"
	"github.com/DulsaraNethmin/curator/internal/store"
)

const testHash = "89599BF4DC369A3A8ECA26411C5CCF922D78B486"

func testMagnet(hash string) string { return "magnet:?xt=urn:btih:" + hash + "&dn=Interstellar" }

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// fakeClient records what it was asked, so a test can assert not just the result
// but the order of the calls that produced it.
type fakeClient struct {
	calls []string

	addErr     error
	byHash     *qbit.Torrent
	byHashErr  error
	torrents   []qbit.Torrent
	listErr    error
	addedMagn  string
	addedCateg string
}

func (f *fakeClient) AddMagnet(_ context.Context, magnet, category string) error {
	f.calls = append(f.calls, "add")
	f.addedMagn, f.addedCateg = magnet, category
	return f.addErr
}

func (f *fakeClient) TorrentByHash(_ context.Context, hash string) (*qbit.Torrent, error) {
	f.calls = append(f.calls, "confirm")
	return f.byHash, f.byHashErr
}

func (f *fakeClient) Torrents(_ context.Context, category string) ([]qbit.Torrent, error) {
	f.calls = append(f.calls, "list")
	return f.torrents, f.listErr
}

// fakeStore records every write, so "nothing was written" is a thing a test can
// actually assert rather than infer.
type fakeStore struct {
	calls []string

	movie      store.Movie
	movieErr   error
	inserted   store.Download
	insertErr  error
	byHash     map[string]store.Download
	getErr     error
	updates    []update
	updateErr  error
	writeCount int
}

type update struct {
	hash        string
	state       string
	progress    float64
	completedAt *time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{byHash: map[string]store.Download{}, movie: store.Movie{ID: 7}}
}

func (f *fakeStore) UpsertWantedMovie(_ context.Context, title string, year int, tmdbID *int64) (store.Movie, error) {
	f.calls = append(f.calls, "upsert-movie")
	f.writeCount++
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

func (f *fakeStore) UpdateDownloadProgress(_ context.Context, hash, state string, progress float64, completedAt *time.Time) error {
	f.calls = append(f.calls, "update")
	f.writeCount++
	// Upper-cased exactly as internal/store does, so this fake cannot pass a test
	// the real store would fail — qbit hands out lower-case hashes and the store
	// stores upper-case ones.
	f.updates = append(f.updates, update{strings.ToUpper(hash), state, progress, completedAt})
	return f.updateErr
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
	client := &fakeClient{byHash: &qbit.Torrent{
		Hash: strings.ToLower(testHash), Name: "Interstellar", State: "downloading", Progress: 0.1,
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
