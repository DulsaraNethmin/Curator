package engine

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestTheEngineOpensNoSocketTheNetworkDidNotHandIt is the only assertion in this
// package made from OUTSIDE the process's own accounting.
//
// Every other test here asks anacrolix what it is holding, or asks a fake what
// it was given. Both are curator reporting on curator. This one asks the
// kernel: every socket the process gained while the engine was built and run
// has to be one the Network handed out, identified by inode rather than by
// shape. It is the automated form of T85's `ss -tunap` reading, which was the
// decisive half of that capture — 70 MB of payload arrived between two socket
// inventories and not one new socket appeared.
//
// # Its blind spot, and it is the reason this does not replace T85
//
// A snapshot sees only sockets that are still open when it is taken. The fifth
// leak — a `udp://` tracker's name resolved by net.ResolveUDPAddr on the host
// (T86) — opens a UDP socket and closes it inside one stdlib call, so an
// inventory either side of it shows nothing at all. That whole class is
// invisible here and is caught only by looking at the wire, which is why
// docs/tasks/T85 is a task and not a paragraph. What this does catch is the
// persistent kind: a listener, a peer conn, a DHT node, an SSDP socket — every
// one of D47's first four leaks.
//
// # Linux only, and deliberately without a seeder
//
// /proc/self/fd is the instrument, so this skips on the laptop and runs on CI
// and on the Pi. There is no second engine in the process on purpose: a seeder
// opens sockets of its own on the same loopback, nothing outside the process
// can attribute a socket to an owner, and the assertion would go noisy for no
// gain. The payload transfer is not what is being measured — T85 established
// that moving 70 MB adds no socket — so what runs here is the part that does
// open them: the client, its listener, its DHT server, and a `udp://` tracker
// announce, which is the one path that asks for a socket per announcer.
func TestTheEngineOpensNoSocketTheNetworkDidNotHandIt(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skipf("/proc/self/fd is the instrument and %s has none; this runs on CI and on the Pi", runtime.GOOS)
	}

	network := &inventory{t: t, inodes: map[uint64]bool{}}

	// Taken as late as possible: everything already open — the test binary's
	// own fds, and anything a previous test in this package has not finished
	// closing — belongs to the before set rather than to the engine.
	before := socketInodes(t)

	e := start(t, Config{DataDir: t.TempDir(), Category: "curator", Network: network})

	_, mi, ih := fixture(t)
	// The udp:// tracker is what makes this worth running: an announcer asks
	// for a packet socket of its own, per family, and that is the request a
	// leak would answer from the host stack instead of from the tunnel.
	if err := e.AddMagnet(context.Background(), magnetFor(t, mi, ih)+udpTracker, "curator"); err != nil {
		t.Fatalf("AddMagnet: %v", err)
	}

	// Long enough for the announcers, the DHT server and anything anacrolix
	// starts on a timer to have asked for what they want. Nothing here waits on
	// a result: the tracker host is unresolvable on purpose, and a leak would
	// have opened its socket before finding that out.
	time.Sleep(2 * time.Second)

	after := socketInodes(t)
	var strangers []string
	for inode := range after {
		if before[inode] || network.handed(inode) {
			continue
		}
		strangers = append(strangers, describeSocket(inode))
	}
	if len(strangers) > 0 {
		t.Errorf("the process holds %d socket(s) the tunnel never handed out: %s\n"+
			"every one of them is a way out of this machine that the kill switch does not cover",
			len(strangers), strings.Join(strangers, ", "))
	}

	// The negative half of the same reading: if the Network was never asked for
	// anything, the snapshots would agree for the wrong reason and this would
	// pass on an engine that had done nothing at all.
	if got := network.count(); got == 0 {
		t.Error("the Network handed out no socket, so the engine never started one and this proves nothing")
	}
}

// inventory is a Network that records the inode of every socket it hands out,
// which is the only identity a socket has that /proc/self/fd also knows.
type inventory struct {
	t *testing.T

	mu     sync.Mutex
	inodes map[uint64]bool
}

func (n *inventory) ListenPacket(_ context.Context, network, address string) (net.PacketConn, error) {
	pc, err := net.ListenPacket(network, address)
	if err != nil {
		return nil, err
	}
	n.record(inodeOf(n.t, pc))
	return pc, nil
}

func (n *inventory) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	n.record(inodeOf(n.t, conn))
	return conn, nil
}

// Nothing may be resolved: a hermetic engine bootstraps nowhere, and the one
// tracker here is meant to stay unresolvable. Answering would open a socket
// this test would then have to explain.
func (n *inventory) LookupHost(context.Context, string) ([]string, error) {
	return nil, fmt.Errorf("inventory resolves nothing")
}

func (n *inventory) record(inode uint64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.inodes[inode] = true
}

func (n *inventory) handed(inode uint64) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.inodes[inode]
}

func (n *inventory) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.inodes)
}

// inodeOf reads the inode behind an open socket. Both halves of the comparison
// have to be the same kind of fact — an address can be reused and a port can be
// guessed at, but an inode names one socket and nothing else.
func inodeOf(t *testing.T, conn any) uint64 {
	t.Helper()
	sc, ok := conn.(syscall.Conn)
	if !ok {
		t.Fatalf("%T is not a syscall.Conn, so the socket it holds cannot be identified", conn)
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		t.Fatalf("SyscallConn: %v", err)
	}
	var inode uint64
	var statErr error
	if err := raw.Control(func(fd uintptr) {
		var st syscall.Stat_t
		if statErr = syscall.Fstat(int(fd), &st); statErr == nil {
			inode = uint64(st.Ino)
		}
	}); err != nil {
		t.Fatalf("Control: %v", err)
	}
	if statErr != nil {
		t.Fatalf("Fstat: %v", statErr)
	}
	return inode
}

// socketInodes is `ss -tunap` for this process and nothing else: every fd whose
// link target is a socket, by inode. A file, a pipe or an eventfd is not one.
func socketInodes(t *testing.T) map[uint64]bool {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatalf("/proc/self/fd: %v", err)
	}
	found := map[uint64]bool{}
	for _, entry := range entries {
		// Racy by nature and harmlessly so: reading the directory opens an fd of
		// its own, and any fd may be closed between the listing and the
		// readlink. A vanished one is not a socket this process holds.
		target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if err != nil {
			continue
		}
		if inode, ok := socketInode(target); ok {
			found[inode] = true
		}
	}
	return found
}

// socketInode reads `socket:[12345]`, which is what a socket fd links to.
func socketInode(target string) (uint64, bool) {
	rest, ok := strings.CutPrefix(target, "socket:[")
	if !ok {
		return 0, false
	}
	rest, ok = strings.CutSuffix(rest, "]")
	if !ok {
		return 0, false
	}
	inode, err := strconv.ParseUint(rest, 10, 64)
	return inode, err == nil
}

// describeSocket turns an inode into something a person can act on, by finding
// it in /proc/net the way ss does. Best effort on purpose: this runs only on
// the failure path, and an inode with no row is still worth reporting — it
// means the socket was closed between the two readings, which is itself the
// answer to "what was that".
func describeSocket(inode uint64) string {
	for _, proto := range []string{"udp", "udp6", "tcp", "tcp6"} {
		body, err := os.ReadFile("/proc/net/" + proto)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(body), "\n")[1:] {
			fields := strings.Fields(line)
			// sl, local, remote, st, queues, timers, retrnsmt, uid, timeout, inode
			if len(fields) < 10 || fields[9] != strconv.FormatUint(inode, 10) {
				continue
			}
			return fmt.Sprintf("%s %s", proto, hexAddr(fields[1]))
		}
	}
	return fmt.Sprintf("inode %d (already closed, so /proc/net no longer lists it)", inode)
}

// hexAddr decodes /proc/net's `0100007F:1F90`. The address is a sequence of
// 32-bit words in HOST byte order, which is little-endian everywhere curator
// runs, so each group of eight hex digits reverses.
func hexAddr(field string) string {
	host, port, ok := strings.Cut(field, ":")
	if !ok || len(host)%8 != 0 {
		return field
	}
	raw := make([]byte, 0, len(host)/2)
	for word := 0; word < len(host); word += 8 {
		for octet := 8; octet > 0; octet -= 2 {
			value, err := strconv.ParseUint(host[word+octet-2:word+octet], 16, 8)
			if err != nil {
				return field
			}
			raw = append(raw, byte(value))
		}
	}
	number, err := strconv.ParseUint(port, 16, 32)
	if err != nil {
		return field
	}
	return net.JoinHostPort(net.IP(raw).String(), strconv.FormatUint(number, 10))
}
