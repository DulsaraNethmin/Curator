package vpn

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Guard is the check curator runs before dispatching. It is deliberately the
// same shape whichever backend is in use, and deliberately NOT the same
// promise — what it can say is decided by whether curator owns the socket.
type Guard func(ctx context.Context) error

// Required refuses every dispatch, naming the variable that would fix it.
//
// It is what a curator with the embedded engine and no tunnel gets, and it is
// the posture an unset QBIT_USER has had since phase 3: the library still
// scans, search still works, and only dispatch reports itself unavailable. A
// mandatory VPN whose default is "off" is a slogan, so the default here is a
// refusal and VPN_REQUIRED=false is the documented way out.
func Required() Guard {
	return func(context.Context) error {
		return fmt.Errorf("no VPN is configured: set VPN_CONFIG (or VPN_CONFIG_FILE), " +
			"or set VPN_REQUIRED=false to download without one")
	}
}

// Tunnelled checks that the tunnel is up and that it changes where traffic
// leaves from.
//
// The answer is cached, because it is a property of the tunnel rather than of
// the request: dispatching six films should not make six round trips to an
// IP-echo service, and a tunnel that was proved good a minute ago is still
// good. A failure is NOT cached — that one is worth asking again, because it is
// what somebody is actively trying to fix.
func Tunnelled(t *Tunnel, checkURL string, host *http.Client, ttl time.Duration, log *slog.Logger) Guard {
	if log == nil {
		log = slog.Default()
	}
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}

	var (
		mu       sync.Mutex
		goodTill time.Time
	)
	return func(ctx context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		if time.Now().Before(goodTill) {
			return nil
		}

		status, err := t.Status()
		if err != nil {
			return err
		}
		if !status.Handshaken() {
			return fmt.Errorf("the VPN tunnel has never completed a handshake with %s", status.Endpoint)
		}

		if _, err := t.CheckExit(ctx, checkURL, host); err != nil {
			return err
		}

		// Not logged with the address in it: GET /api/logs is readable by
		// anyone on the LAN (docs/decisions.md D18), and this is the one fact
		// the tunnel exists to keep.
		log.Info("vpn check passed: the tunnel is up and traffic leaves from somewhere else")
		goodTill = time.Now().Add(ttl)
		return nil
	}
}

// External is the honest floor for a torrent client curator does not route.
//
// It cannot protect that client, so it checks it: `clientAddress` is what the
// client says it looks like from the swarm, and if that equals curator's own
// exit address then it is not behind anything and a dispatch would leave from
// the address a VPN was installed to hide.
//
// **Empty passes, with a warning.** A client that has never talked to a swarm
// has never learned its address, and refusing the first dispatch on a fact that
// only exists after one is a deadlock with a security-shaped excuse. Saying so
// out loud is the honest half of that.
func External(clientAddress func(context.Context) (string, error), checkURL string, host *http.Client, log *slog.Logger) Guard {
	if log == nil {
		log = slog.Default()
	}
	if host == nil {
		host = &http.Client{Timeout: ipCheckTimeout}
	}

	return func(ctx context.Context) error {
		theirs, err := clientAddress(ctx)
		if err != nil {
			return fmt.Errorf("could not ask the torrent client where it appears from: %w", err)
		}
		if strings.TrimSpace(theirs) == "" {
			log.Warn("the torrent client has not learned its own external address yet, " +
				"so curator cannot tell whether it is behind a VPN; dispatching anyway")
			return nil
		}

		ours, err := exitAddress(ctx, host, orDefault(checkURL))
		if err != nil {
			return fmt.Errorf("could not establish curator's own exit address to compare: %w", err)
		}
		if theirs == ours {
			return fmt.Errorf("the torrent client leaves from the same address curator does (%s), "+
				"so curator cannot tell its traffic apart from this machine's own and can promise "+
				"nothing about it — put that client behind its own VPN, run curator's engine with "+
				"TORRENT_BACKEND=embedded, which curator routes itself, or accept it with "+
				"VPN_REQUIRED=false", ours)
		}
		return nil
	}
}

// Advisory turns a refusal into a warning, once per dispatch.
//
// It is what VPN_REQUIRED=false means for a check curator cannot enforce: the
// operator has been told what cannot be verified and has said to proceed. It
// keeps saying it rather than falling silent, because the whole difference
// between an accepted risk and a forgotten one is whether anything still
// mentions it.
func Advisory(g Guard, log *slog.Logger) Guard {
	if log == nil {
		log = slog.Default()
	}
	return func(ctx context.Context) error {
		if err := g(ctx); err != nil {
			log.Warn("dispatching anyway because VPN_REQUIRED=false", "unverified", err)
		}
		return nil
	}
}

func orDefault(url string) string {
	if strings.TrimSpace(url) == "" {
		return DefaultIPCheckURL
	}
	return url
}
