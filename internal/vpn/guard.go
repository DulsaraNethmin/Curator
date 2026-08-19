package vpn

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
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

// tunnelState is the half of *Tunnel a check needs: what the device says about
// itself, and where traffic actually comes out.
//
// An interface rather than the concrete type so that the caching can be tested
// at all. *Tunnel needs a real WireGuard device and a real endpoint, so with it
// hard-wired the only reachable assertions were on a live tunnel — which is why
// the ten-minute hole T82 closed survived from phase 6 without a test ever going
// near it.
type tunnelState interface {
	Status() (Status, error)
	CheckExit(ctx context.Context, url string, host *http.Client) (string, error)
}

// Tunnelled is the dispatch check, over a Checker.
//
// It is three lines because the deciding is all in one place now. There are two
// callers of that decision — this, and the sentinel that watches while nobody is
// looking — and two implementations of "is this tunnel good" would eventually
// disagree, at which point the screen and the refusal say different things about
// the same tunnel and neither is obviously wrong.
//
// The Checker is passed in rather than built here so that both callers share one
// cache: the sentinel's periodic proof is the same proof a dispatch a moment
// later would otherwise pay for again.
func Tunnelled(c *Checker) Guard {
	return func(ctx context.Context) error {
		return c.Check(ctx, false).Err()
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

// Enforced makes a check's refusal conditional on enforcement being ON, asked
// per dispatch rather than at start-up.
//
// It replaces Advisory, which this deletes. That did the same downgrade but was
// chosen at start-up and applied to the qBittorrent branch only — so
// VPN_REQUIRED=false still refused a dispatch through a broken tunnel on the
// embedded engine, which is the default. One rule, both backends, read live.
//
// enforcing is a function and not a bool for the whole point of this: the
// setting is Immediate now, so it is re-read on the next request after a write
// rather than at the next start.
func Enforced(g Guard, enforcing func() bool, log *slog.Logger) Guard {
	if log == nil {
		log = slog.Default()
	}
	if enforcing == nil {
		// A missing holder enforces. Failing open here would mean a wiring
		// mistake silently switching the kill switch off.
		enforcing = func() bool { return true }
	}
	return func(ctx context.Context) error {
		err := g(ctx)
		if err == nil || enforcing() {
			return err
		}
		// Kept saying, every time, rather than falling silent — the whole
		// difference between an accepted risk and a forgotten one is whether
		// anything still mentions it.
		log.Warn("dispatching anyway because VPN_REQUIRED is off", "unverified", err)
		return nil
	}
}

func orDefault(url string) string {
	if strings.TrimSpace(url) == "" {
		return DefaultIPCheckURL
	}
	return url
}
