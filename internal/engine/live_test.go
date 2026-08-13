package engine

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/DulsaraNethmin/curator/internal/torrent"
)

// debianMagnet is the payload every measurement in phase 6 used: 755.0 MB in
// 3020 × 256 KiB pieces. Keeping the same one is what makes the numbers
// comparable across the spike, this engine, and the Pi at phase 10.
const debianMagnet = "magnet:?xt=urn:btih:481b6e3617be4c88f96cb25e47c9d8272130071e" +
	"&dn=debian-13.6.0-amd64-netinst.iso" +
	"&tr=http%3A%2F%2Fbttracker.debian.org%3A6969%2Fannounce"

// TestLiveDownloadPeakRSS is the memory measurement, and the only test here that
// touches a swarm.
//
// It is off unless CURATOR_LIVE_TORRENT=1, because it downloads 755 MB from
// strangers and takes a couple of minutes — not something a `go test ./...`
// should do. It is a test rather than a throwaway program so that the number can
// be taken again, on the hardware that actually matters, by running one command:
//
//	CURATOR_LIVE_TORRENT=1 go test -run TestLiveDownloadPeakRSS -timeout 20m ./internal/engine
//
// Peak RSS is the one number in this phase that could still lose the Pi: it has
// 8 GB and is also running Jellyfin, and the spike measured 822 MB for this
// payload at anacrolix's own defaults — 50 conns and 64 MiB of unverified
// bytes. This engine sets both lower (see DefaultMaxConns and unverifiedBytes),
// and this is what says whether that worked.
func TestLiveDownloadPeakRSS(t *testing.T) {
	if testing.Short() || os.Getenv("CURATOR_LIVE_TORRENT") != "1" {
		t.Skip("set CURATOR_LIVE_TORRENT=1 to download 755 MB from a real swarm")
	}

	dataDir := t.TempDir()
	e := start(t, Config{DataDir: dataDir, Category: "curator"})

	ctx := context.Background()
	if err := e.AddMagnet(ctx, debianMagnet, "curator"); err != nil {
		t.Fatalf("AddMagnet: %v", err)
	}

	const budget = 15 * time.Minute
	started := time.Now()
	deadline := started.Add(budget)
	var peakHeap float64
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)

		found, err := e.Torrents(ctx, "curator")
		if err != nil {
			t.Fatalf("Torrents: %v", err)
		}
		if len(found) == 0 {
			continue
		}
		got := found[0]
		heap, heapSys := heapMB()
		if heap > peakHeap {
			peakHeap = heap
		}
		t.Logf("%5.0fs  %-11s %5.1f%%  peak RSS %6.1f MB  heap %5.1f MB (sys %5.1f)",
			time.Since(started).Seconds(), got.State, got.Progress*100, peakRSS(), heap, heapSys)

		if got.State != torrent.StateCompleted {
			continue
		}

		rss := peakRSS()
		t.Logf("completed in %.1fs at %.2f MB/s; PEAK RSS %.1f MB, PEAK GO HEAP %.1f MB, for a 755 MB payload",
			time.Since(started).Seconds(), 755/time.Since(started).Seconds(), rss, peakHeap)

		// The payload is where ContentPath says it is, which is the promise the
		// importer hardlinks from.
		if _, err := os.Stat(got.ContentPath); err != nil {
			t.Errorf("ContentPath %q: %v", got.ContentPath, err)
		}
		// The finished file is 0444 (measured in the spike), which retires
		// docs/phase-4.md's "qBittorrent writes 0644" conclusion. A hardlink
		// carries the source's bits, so this is what the library copy gets.
		if entries, err := os.ReadDir(got.ContentPath); err == nil && len(entries) > 0 {
			if info, err := os.Stat(filepath.Join(got.ContentPath, entries[0].Name())); err == nil {
				t.Logf("finished file mode: %v", info.Mode().Perm())
			}
		}
		// The budget is on the GO HEAP, not on RSS, and the difference is the
		// whole finding: RSS tracks payload size ~1:1 because the pages are the
		// kernel's copy of a file being written, which is reclaimable under
		// pressure. Anonymous heap is what an OOM killer actually counts.
		if peakHeap > heapBudget {
			t.Errorf("peak Go heap %.1f MB exceeds the %.0f MB budget for a 755 MB payload; "+
				"record the number and pick a lever rather than deleting this line", peakHeap, heapBudget)
		}
		return
	}
	t.Fatalf("the payload did not complete within %s", budget)
}

// heapBudget is what this engine may hold in anonymous memory while downloading
// any payload, in MB. It is a constant rather than a fraction of the file
// because that is the claim: heap must not track payload size.
const heapBudget float64 = 400

// heapMB reports Go's live heap and the heap it has taken from the OS.
func heapMB() (alloc, sys float64) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return float64(ms.HeapAlloc) / (1 << 20), float64(ms.HeapSys) / (1 << 20)
}

// peakRSS reports the high-water mark of resident memory in MB. ru_maxrss is
// bytes on darwin and kilobytes on linux, which is a difference that has
// silently produced 1024× wrong numbers in other people's benchmarks.
func peakRSS() float64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	scale := float64(1)
	if runtime.GOOS == "linux" {
		scale = 1024
	}
	return float64(ru.Maxrss) * scale / (1 << 20)
}
