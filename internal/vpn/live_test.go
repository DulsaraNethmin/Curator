package vpn

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLiveTunnel is the only test here that brings a device up, and it needs a
// real provider config to do it. It follows the same contract as
// internal/jellyfin's and internal/tmdb's live checks: skip under -short, skip
// when there is nothing configured, but FAIL on a bad answer, so a tunnel that
// has stopped working cannot sit there green.
//
//	VPN_CONFIG_FILE=~/wg0.conf go test -run TestLiveTunnel -v ./internal/vpn
//
// What it proves is the part that cannot be proved hermetically: that the
// handshake completes, that UDP crosses a userspace stack, and — the whole
// point — that traffic leaves from somewhere that is not this machine.
func TestLiveTunnel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the live tunnel check in -short mode")
	}
	text := liveConfig(t)
	if text == "" {
		t.Skip("no VPN_CONFIG or VPN_CONFIG_FILE in the environment or ../../.env; skipping")
	}

	cfg, err := ParseConfig(text)
	if err != nil {
		t.Fatalf("ParseConfig: %v", err)
	}

	tunnel, err := New(cfg, quiet())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer tunnel.Close()

	// The handshake is the far end's to complete, so it is waited for rather
	// than asserted immediately.
	deadline := time.Now().Add(20 * time.Second)
	var status Status
	for time.Now().Before(deadline) {
		if status, err = tunnel.Status(); err != nil {
			t.Fatalf("Status: %v", err)
		}
		if status.Handshaken() {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if !status.Handshaken() {
		t.Fatalf("no handshake with %s within 20s", status.Endpoint)
	}
	t.Logf("handshaken with %s, rx %d B, tx %d B", status.Endpoint, status.Received, status.Sent)

	// UDP over the netstack. uTP, DHT and UDP trackers are all one of these,
	// and it is the part T33 measured first because it was the part most likely
	// to disappoint.
	pc, err := tunnel.ListenPacket(context.Background(), "udp", ":0")
	if err != nil {
		t.Fatalf("ListenPacket over the tunnel: %v", err)
	}
	pc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	address, err := tunnel.CheckExit(ctx, "", nil)
	switch {
	case errors.Is(err, ErrSameExit):
		t.Fatalf("the tunnel is up but traffic still leaves from this machine's address: %v", err)
	case err != nil:
		t.Fatalf("CheckExit: %v", err)
	}
	// Logged only because this is a test somebody runs deliberately. Nothing in
	// the running program ever logs this address (D18).
	t.Logf("exit address through the tunnel: %s", address)
}

// liveConfig reads the config from the environment, falling back to the
// gitignored .env at the repo root so the check runs from a plain `go test`
// without exporting anything. Neither the file's contents nor any key in it is
// ever logged.
func liveConfig(t *testing.T) string {
	t.Helper()

	if inline := strings.TrimSpace(os.Getenv("VPN_CONFIG")); inline != "" {
		return inline
	}
	path := strings.TrimSpace(os.Getenv("VPN_CONFIG_FILE"))
	if path == "" {
		if inline := dotEnv(t, "VPN_CONFIG"); inline != "" {
			return inline
		}
		path = dotEnv(t, "VPN_CONFIG_FILE")
	}
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("VPN_CONFIG_FILE %s: %v", path, err)
	}
	return string(body)
}

// dotEnv reads one value out of the repo-root .env, the same way
// internal/jellyfin's live test does. Tests run in the package directory:
// internal/vpn -> repo root.
func dotEnv(t *testing.T, key string) string {
	t.Helper()

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
		if !found || strings.TrimSpace(name) != key {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return ""
}
