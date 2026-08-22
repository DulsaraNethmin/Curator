package qbit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DulsaraNethmin/curator/internal/torrent"
)

// qbFake is a qBittorrent that speaks one of the two vocabularies.
//
// `modern` decides which: a 5.x answers torrents/stop and 404s torrents/pause,
// a 4.x does the reverse. That is the whole point of the fixture — the rename
// is real (qBittorrent 5.0 renamed pause to stop, and state.go has documented
// the matching STATE rename since phase 3) and curator does not get to know
// which server it is pointed at.
func qbFake(t *testing.T, category string, modern bool, hit *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, pathLogin):
			http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "s"})
			fmt.Fprint(w, bodyOK)
		case strings.HasSuffix(path, pathTorrentsInfo):
			fmt.Fprintf(w, `[{"hash":"aaaa","name":"ours","category":%q}]`, category)
		case strings.HasSuffix(path, pathTorrentsStop), strings.HasSuffix(path, pathTorrentsStart):
			*hit = append(*hit, path)
			if !modern {
				http.Error(w, "Not Found", http.StatusNotFound)
				return
			}
			fmt.Fprint(w, bodyOK)
		case strings.HasSuffix(path, pathTorrentsPause), strings.HasSuffix(path, pathTorrentsResume):
			*hit = append(*hit, path)
			if modern {
				http.Error(w, "Not Found", http.StatusNotFound)
				return
			}
			fmt.Fprint(w, bodyOK)
		default:
			http.Error(w, "unexpected "+path, http.StatusTeapot)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A 5.x server takes the modern name and the old one is never tried.
func TestPauseUsesStopOnAModernServer(t *testing.T) {
	var hit []string
	srv := qbFake(t, "curator", true, &hit)

	if err := New(srv.URL, "u", "p", srv.Client()).
		PauseTorrent(context.Background(), "AAAA", "curator"); err != nil {
		t.Fatalf("PauseTorrent: %v", err)
	}
	if len(hit) != 1 || !strings.HasSuffix(hit[0], pathTorrentsStop) {
		t.Errorf("called %v, want only %s", hit, pathTorrentsStop)
	}
}

// **The fallback, and the reason it exists.** A stranger's `docker run` can be a
// 4.x that has never heard of torrents/stop, and a 404 there is not a failure —
// it is the answer to "which vocabulary does this server speak".
func TestPauseFallsBackToTheOldNameOnAnOlderServer(t *testing.T) {
	var hit []string
	srv := qbFake(t, "curator", false, &hit)

	if err := New(srv.URL, "u", "p", srv.Client()).
		PauseTorrent(context.Background(), "AAAA", "curator"); err != nil {
		t.Fatalf("PauseTorrent on a 4.x: %v", err)
	}
	if len(hit) != 2 {
		t.Fatalf("called %v, want the modern name then the legacy one", hit)
	}
	if !strings.HasSuffix(hit[0], pathTorrentsStop) || !strings.HasSuffix(hit[1], pathTorrentsPause) {
		t.Errorf("called %v, want %s then %s", hit, pathTorrentsStop, pathTorrentsPause)
	}
}

func TestResumeFallsBackToo(t *testing.T) {
	var hit []string
	srv := qbFake(t, "curator", false, &hit)

	if err := New(srv.URL, "u", "p", srv.Client()).
		ResumeTorrent(context.Background(), "AAAA", "curator"); err != nil {
		t.Fatalf("ResumeTorrent on a 4.x: %v", err)
	}
	if len(hit) != 2 || !strings.HasSuffix(hit[1], pathTorrentsResume) {
		t.Errorf("called %v, want the start/resume pair", hit)
	}
}

// The guard that replaced "the method does not exist": another app's torrent is
// refused, and nothing is sent.
func TestPauseRefusesAnotherAppsTorrent(t *testing.T) {
	var hit []string
	srv := qbFake(t, "radarr", true, &hit)
	c := New(srv.URL, "u", "p", srv.Client())

	for _, call := range []struct {
		name string
		fn   func(context.Context, string, string) error
	}{
		{"pause", c.PauseTorrent},
		{"resume", c.ResumeTorrent},
	} {
		err := call.fn(context.Background(), "AAAA", "curator")
		if !errors.Is(err, torrent.ErrWrongCategory) {
			t.Errorf("%s = %v, want ErrWrongCategory", call.name, err)
		}
	}
	if len(hit) != 0 {
		t.Errorf("sent %v for a torrent in category radarr — the check must come first", hit)
	}
}

// A hash qBittorrent does not have is not found, and is deliberately not
// DeleteTorrent's "already gone is success".
func TestPausingAHashQBittorrentDoesNotHave(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, pathLogin):
			http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "s"})
			fmt.Fprint(w, bodyOK)
		case strings.HasSuffix(r.URL.Path, pathTorrentsInfo):
			fmt.Fprint(w, `[]`)
		default:
			http.Error(w, "must not be called", http.StatusTeapot)
		}
	}))
	defer srv.Close()

	err := New(srv.URL, "u", "p", srv.Client()).PauseTorrent(context.Background(), "AAAA", "curator")
	if !errors.Is(err, torrent.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// notFound reads `do`'s own rendering of a non-200, so this pins the pair: if
// either the format string or this matcher changes, the 4.x fallback silently
// stops working and every pause against an older server fails.
func TestNotFoundMatchesWhatDoActuallyWrites(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, pathLogin) {
			http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "s"})
			fmt.Fprint(w, bodyOK)
			return
		}
		http.Error(w, "Not Found", http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := New(srv.URL, "u", "p", srv.Client()).
		do(context.Background(), http.MethodPost, pathTorrentsStop, nil, nil)
	if err == nil {
		t.Fatal("a 404 produced no error")
	}
	if !notFound(err) {
		t.Errorf("notFound(%q) = false — the fallback to the 4.x endpoint is dead", err)
	}

	// And it must not swallow every other failure into the fallback path.
	if notFound(errors.New("qbit torrents/stop: qBittorrent answered 500 Internal Server Error")) {
		t.Error("notFound matched a 500")
	}
	if notFound(ErrAuth) {
		t.Error("notFound matched an auth failure — a bad password would retry as a 4.x")
	}
}
