package engine

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/anacrolix/torrent/metainfo"

	"github.com/DulsaraNethmin/curator/internal/torrent"
	"github.com/DulsaraNethmin/curator/internal/vpn"
)

// tunnelConfig reads a wg-quick config from the environment, falling back to
// the gitignored .env at the repo root. Nothing in it is ever logged.
func tunnelConfig(t *testing.T) string {
	t.Helper()

	if inline := strings.TrimSpace(os.Getenv("VPN_CONFIG")); inline != "" {
		return inline
	}
	path := strings.TrimSpace(os.Getenv("VPN_CONFIG_FILE"))
	if path == "" {
		f, err := os.Open(filepath.Join("..", "..", ".env"))
		if err != nil {
			return ""
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(line, "#") {
				continue
			}
			name, value, found := strings.Cut(line, "=")
			if !found {
				continue
			}
			switch strings.TrimSpace(name) {
			case "VPN_CONFIG":
				return strings.Trim(strings.TrimSpace(value), `"'`)
			case "VPN_CONFIG_FILE":
				path = strings.Trim(strings.TrimSpace(value), `"'`)
			}
		}
	}
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("VPN_CONFIG_FILE %s: %v", path, err)
	}
	return string(body)
}

// debianMagnet is the payload every measurement in phase 6 used: 755.0 MB in
// 3020 × 256 KiB pieces. Keeping the same one is what makes the numbers
// comparable across the spike, this engine, and the Pi at phase 10.
const debianInfoHash = "481b6e3617be4c88f96cb25e47c9d8272130071e"

const debianMagnet = "magnet:?xt=urn:btih:" + debianInfoHash +
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

// TestLiveEngineOverTunnel is the phase's whole claim in one test: peer traffic
// leaving through a WireGuard tunnel curator brought up itself, with a client
// that has no socket of its own to leak through.
//
// It needs a real tunnel, so it runs only when VPN_CONFIG_FILE (or VPN_CONFIG)
// is set — the same contract internal/vpn's live test uses:
//
//	VPN_CONFIG_FILE=~/wg0.conf go test -run TestLiveEngineOverTunnel -v ./internal/engine
//
// It stops at the first megabytes rather than downloading all 755 MB. What is
// under test is whether bytes can move at all through a netstack the client is
// confined to: metadata from the swarm means DHT or a tracker answered, and
// payload means uTP carried data. The full-payload version of this was T33's
// spike, and its numbers are in docs/phase-6.md.
func TestLiveEngineOverTunnel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the live tunnel check in -short mode")
	}
	text := tunnelConfig(t)
	if text == "" {
		t.Skip("no VPN_CONFIG or VPN_CONFIG_FILE in the environment or ../../.env; skipping")
	}

	cfg, err := vpn.ParseConfig(text)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}
	tunnel, err := vpn.New(cfg, quiet())
	if err != nil {
		t.Fatalf("vpn.New: %v", err)
	}
	// Registered BEFORE the engine's own cleanup, so it runs after it: cleanups
	// are LIFO, and closing the tunnel under a live engine makes the uTP socket
	// print "error reading Socket PacketConn: EOF" as its read loop dies. main
	// gets this right by the same rule with defers; this test has to model it
	// rather than produce noise nobody can place.
	t.Cleanup(func() { _ = tunnel.Close() })

	e := start(t, Config{DataDir: t.TempDir(), Category: "curator", Network: tunnel})
	if e.socket == nil {
		t.Fatal("no shared uTP socket was opened on the tunnel")
	}

	ctx := context.Background()
	if err := e.AddMagnet(ctx, debianMagnet, "curator"); err != nil {
		t.Fatalf("AddMagnet: %v", err)
	}

	const want = 8 << 20 // enough to prove peers are sending, not enough to be a download
	started := time.Now()
	var gotMetadata time.Duration
	deadline := time.Now().Add(5 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)

		found, err := e.TorrentByHash(ctx, torrent.NormalizeHash(debianInfoHash))
		if err != nil {
			t.Fatalf("TorrentByHash: %v", err)
		}
		if found == nil {
			continue
		}
		lt, ok := e.client.Torrent(mustHash(t, debianInfoHash))
		if !ok {
			t.Fatal("the torrent vanished from the client")
		}
		if gotMetadata == 0 && lt.Info() != nil {
			gotMetadata = time.Since(started)
			t.Logf("metadata through the tunnel in %s", gotMetadata.Round(time.Millisecond))
		}
		stats := lt.Stats()
		read := stats.BytesReadData.Int64()
		t.Logf("%4.0fs  %-11s %5.2f%%  %6.1f MB from %d peers",
			time.Since(started).Seconds(), found.State, found.Progress*100,
			float64(read)/(1<<20), stats.ActivePeers)

		if read >= want {
			t.Logf("PASS  %.1f MB carried entirely over the tunnel in %s — the client opened no socket of its own",
				float64(read)/(1<<20), time.Since(started).Round(time.Second))
			return
		}
	}
	t.Fatalf("less than %d MB moved through the tunnel in 5 minutes", want>>20)
}

func mustHash(t *testing.T, hex string) metainfo.Hash {
	t.Helper()
	var ih metainfo.Hash
	if err := ih.FromHexString(hex); err != nil {
		t.Fatal(err)
	}
	return ih
}
