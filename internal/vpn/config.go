// Package vpn owns a WireGuard tunnel that lives inside this process.
//
// It is a userspace device on a gVisor netstack TUN: nothing outside this
// process sees an interface, which is why it needs no NET_ADMIN, no privileged
// container and no sidecar, and why the credentials can live in the app where
// the person configuring them can reach them (docs/decisions.md D27).
//
// **Only the torrent engine is bound to it.** The web UI, TMDB, the indexers,
// minter and Jellyfin all keep the host's stack — deliberately, because a bad
// tunnel config must never be able to lock somebody out of the screen that
// fixes the tunnel. The honest consequence is that this phase protects peer
// traffic and nothing else: a 1337x search still leaves from the host address.
//
// The kill switch is structural rather than configured. The engine is built
// with DisableTCP, DisableUTP and NoDHT, so it has no socket of its own and
// every byte has to go through the dialers here; a dead tunnel is therefore a
// failed dial. One fallback to net.Dial anywhere would turn that guarantee back
// into a hope.
package vpn

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

// DefaultMTU is wg-quick's own default for a tunnel over IPv4: 1500 minus 60
// bytes of WireGuard and outer IP header. A provider that needs another value
// says so in its config file.
const DefaultMTU = 1420

// Config is one wg-quick `.conf`, parsed. It is what a provider hands out and
// what phase 7's Settings screen will accept in a textarea — one parser serves
// both, which is why the input is the file rather than a dozen fields.
type Config struct {
	PrivateKey string // base64, as the file spells it
	Addresses  []netip.Addr
	DNS        []netip.Addr
	MTU        int

	PeerPublicKey string
	PresharedKey  string
	Endpoint      string // host:port, exactly as written
	AllowedIPs    []netip.Prefix
	Keepalive     int
}

// ParseConfig reads a wg-quick configuration file.
//
// Only the keys a userspace tunnel can act on are read. wg-quick's PostUp,
// Table, SaveConfig and friends describe what to do to a kernel interface and
// a routing table, neither of which exists here; they are ignored rather than
// rejected, because a provider's file carrying one should still work.
func ParseConfig(text string) (Config, error) {
	var (
		cfg     Config
		section string
	)

	for n, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			section = strings.ToLower(strings.Trim(line, "[]"))
			continue
		}

		key, value, found := strings.Cut(line, "=")
		if !found {
			return Config{}, fmt.Errorf("vpn config line %d: %q is not `key = value`", n+1, line)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		// A trailing comment is legal in these files and would otherwise become
		// part of a key.
		if cut := strings.IndexAny(value, "#;"); cut >= 0 {
			value = strings.TrimSpace(value[:cut])
		}

		if err := cfg.set(section, key, value); err != nil {
			return Config{}, fmt.Errorf("vpn config line %d: %w", n+1, err)
		}
	}

	if cfg.MTU == 0 {
		cfg.MTU = DefaultMTU
	}
	if len(cfg.AllowedIPs) == 0 {
		// wg-quick's own default for a full-tunnel peer, and the only sensible
		// one here: this device carries the torrent engine's traffic and
		// nothing else, so there is nothing to route around it.
		cfg.AllowedIPs = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}
	}
	return cfg, cfg.validate()
}

func (c *Config) set(section, key, value string) error {
	switch section {
	case "interface":
		switch key {
		case "privatekey":
			c.PrivateKey = value
		case "address":
			addrs, err := parseAddrs(value)
			if err != nil {
				return fmt.Errorf("Address: %w", err)
			}
			c.Addresses = append(c.Addresses, addrs...)
		case "dns":
			addrs, err := parseAddrs(value)
			if err != nil {
				return fmt.Errorf("DNS: %w", err)
			}
			c.DNS = append(c.DNS, addrs...)
		case "mtu":
			mtu, err := strconv.Atoi(value)
			if err != nil || mtu < 576 || mtu > 65535 {
				return fmt.Errorf("MTU %q: want a number between 576 and 65535", value)
			}
			c.MTU = mtu
		}
	case "peer":
		switch key {
		case "publickey":
			c.PeerPublicKey = value
		case "presharedkey":
			c.PresharedKey = value
		case "endpoint":
			c.Endpoint = value
		case "allowedips":
			for _, field := range strings.Split(value, ",") {
				prefix, err := netip.ParsePrefix(strings.TrimSpace(field))
				if err != nil {
					return fmt.Errorf("AllowedIPs %q: %w", field, err)
				}
				c.AllowedIPs = append(c.AllowedIPs, prefix)
			}
		case "persistentkeepalive":
			seconds, err := strconv.Atoi(value)
			if err != nil || seconds < 0 {
				return fmt.Errorf("PersistentKeepalive %q: want a whole number of seconds", value)
			}
			c.Keepalive = seconds
		}
	}
	return nil
}

// parseAddrs reads `Address = 10.0.0.2/32, fd00::2/128`, discarding the prefix
// length: a userspace stack is given the addresses it answers to, and the mask
// describes a route it does not have.
func parseAddrs(value string) ([]netip.Addr, error) {
	var addrs []netip.Addr
	for _, field := range strings.Split(value, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		if prefix, err := netip.ParsePrefix(field); err == nil {
			addrs = append(addrs, prefix.Addr())
			continue
		}
		addr, err := netip.ParseAddr(field)
		if err != nil {
			return nil, fmt.Errorf("%q is not an address", field)
		}
		addrs = append(addrs, addr)
	}
	return addrs, nil
}

// validate refuses a config that would bring up a device which can never
// handshake. Each of these is a silent failure otherwise: the device comes up,
// dials go nowhere, and the only symptom is a download that never starts.
func (c Config) validate() error {
	switch {
	case c.PrivateKey == "":
		return fmt.Errorf("vpn config: [Interface] PrivateKey is missing")
	case c.PeerPublicKey == "":
		return fmt.Errorf("vpn config: [Peer] PublicKey is missing")
	case c.Endpoint == "":
		return fmt.Errorf("vpn config: [Peer] Endpoint is missing")
	case len(c.Addresses) == 0:
		return fmt.Errorf("vpn config: [Interface] Address is missing, so the tunnel has no address of its own")
	}
	if _, _, err := net.SplitHostPort(c.Endpoint); err != nil {
		return fmt.Errorf("vpn config: Endpoint %q needs a port, as in vpn.example.com:51820", c.Endpoint)
	}
	if _, err := key(c.PrivateKey); err != nil {
		return fmt.Errorf("vpn config: PrivateKey: %w", err)
	}
	if _, err := key(c.PeerPublicKey); err != nil {
		return fmt.Errorf("vpn config: [Peer] PublicKey: %w", err)
	}
	if c.PresharedKey != "" {
		if _, err := key(c.PresharedKey); err != nil {
			return fmt.Errorf("vpn config: PresharedKey: %w", err)
		}
	}
	return nil
}

// key converts one base64 WireGuard key into the hex the device's UAPI speaks.
// The conversion happens once, here, and the result is never logged.
func key(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return "", fmt.Errorf("not base64")
	}
	if len(raw) != 32 {
		return "", fmt.Errorf("is %d bytes decoded, want 32", len(raw))
	}
	return hex.EncodeToString(raw), nil
}

// uapi renders the configuration in the line protocol wireguard-go's IpcSet
// reads. endpoint must already be an IP:port — see resolveEndpoint for why.
func (c Config) uapi(endpoint string) (string, error) {
	private, err := key(c.PrivateKey)
	if err != nil {
		return "", err
	}
	public, err := key(c.PeerPublicKey)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\n", private)
	b.WriteString("replace_peers=true\n")
	fmt.Fprintf(&b, "public_key=%s\n", public)
	if c.PresharedKey != "" {
		preshared, err := key(c.PresharedKey)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "preshared_key=%s\n", preshared)
	}
	fmt.Fprintf(&b, "endpoint=%s\n", endpoint)
	if c.Keepalive > 0 {
		fmt.Fprintf(&b, "persistent_keepalive_interval=%d\n", c.Keepalive)
	}
	b.WriteString("replace_allowed_ips=true\n")
	for _, prefix := range c.AllowedIPs {
		fmt.Fprintf(&b, "allowed_ip=%s\n", prefix.String())
	}
	return b.String(), nil
}
