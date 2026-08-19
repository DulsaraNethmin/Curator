package engine

import (
	"context"
	"reflect"
	"sort"
	"strings"
	"testing"

	anacrolix "github.com/anacrolix/torrent"
)

// How every field of anacrolix's ClientConfig can or cannot put a byte on the
// wire. Four answers, and the third one is the reason this file exists.
//
//	bound     curator points it at the Network, so traffic through it is
//	          traffic through the tunnel
//	disabled  the kill switch turns the path off outright
//	nil       it MUST stay at its zero value: a non-zero value REPLACES a
//	          binding curator made, rather than adding to it
//	inert     it cannot originate a connection — storage, logging, timeouts,
//	          rate limits, identity, or a knob on a socket that is already ours
//
// The ones worth reading twice:
//
//   - WebTransport and MetainfoSourcesClient are `nil` rather than `inert`, and
//     that is not pedantry. client.go:284-295 assigns cl.httpClient.Transport =
//     cfg.WebTransport and builds the transport carrying HTTPDialContext ONLY if
//     that is nil, so setting WebTransport silently voids the webseed and
//     metainfo binding without touching HTTPDialContext at all. Same shape at
//     client.go:296 for MetainfoSourcesClient. Nothing in curator sets either,
//     and nothing may.
//   - DisableWebseeds is `inert` BECAUSE HTTPDialContext binds webseeds. It is
//     deliberately not set: they are a real source of bytes and they are inside
//     the tunnel.
//   - LookupTrackerIp is declared and never called anywhere in v1.61.0 — its own
//     comment says "Deprecated ... TODO: Wire back into UDP tracker client
//     implementation". It is inert today and would be an egress path the day
//     that TODO is done, which is exactly the day this test is meant to fire.
//   - ICEServers and ICEServerList are STUN/TURN servers reached only by
//     WebTorrent, which is now disabled. They become live again if
//     DisableWebtorrent is ever unset.
//   - HTTPProxy and HttpRequestDirector shape a request on a transport whose
//     DialContext is already ours; the proxy itself is dialled through it.
//   - UpnpID names a mapping that NoDefaultPortForwarding stops us asking for.
var egressClassification = map[string]string{
	// bound — through the Network
	"HTTPDialContext":     "bound",
	"TrackerDialContext":  "bound",
	"TrackerListenPacket": "bound",
	"DhtStartingNodes":    "bound",

	// disabled — the kill switch
	"DisableTCP":              "disabled",
	"DisableUTP":              "disabled",
	"NoDHT":                   "disabled",
	"DisableWebtorrent":       "disabled",
	"NoDefaultPortForwarding": "disabled",
	"DisableIPv4":             "disabled",
	"DisableIPv6":             "disabled",

	// nil — a value here replaces a binding rather than adding to one
	"WebTransport":          "nil",
	"MetainfoSourcesClient": "nil",

	// inert
	"AcceptPeerConnections":             "inert",
	"AlwaysWantConns":                   "inert",
	"Bep20":                             "inert",
	"Callbacks":                         "inert",
	"ClientDhtConfig":                   "inert",
	"ClientTrackerConfig":               "inert",
	"ConfigureAnacrolixDhtServer":       "inert",
	"CryptoProvides":                    "inert",
	"CryptoSelector":                    "inert",
	"DHTOnQuery":                        "inert",
	"DataDir":                           "inert",
	"Debug":                             "inert",
	"DefaultStorage":                    "inert",
	"DialForPeerConns":                  "inert",
	"DialRateLimiter":                   "inert",
	"DisableAcceptRateLimiting":         "inert",
	"DisableAggressiveUpload":           "inert",
	"DisableIPv4Peers":                  "inert",
	"DisablePEX":                        "inert",
	"DisableTrackers":                   "inert",
	"DisableWebseeds":                   "inert",
	"DownloadRateLimiter":               "inert",
	"DropDuplicatePeerIds":              "inert",
	"DropMutuallyCompletePeers":         "inert",
	"EstablishedConnsPerTorrent":        "inert",
	"ExtendedHandshakeClientVersion":    "inert",
	"Extensions":                        "inert",
	"HTTPProxy":                         "inert",
	"HTTPUserAgent":                     "inert",
	"HalfOpenConnsPerTorrent":           "inert",
	"HandshakesTimeout":                 "inert",
	"HeaderObfuscationPolicy":           "inert",
	"HttpRequestDirector":               "inert",
	"ICEServerList":                     "inert",
	"ICEServers":                        "inert",
	"IPBlocklist":                       "inert",
	"KeepAliveTimeout":                  "inert",
	"ListenHost":                        "inert",
	"ListenPort":                        "inert",
	"Logger":                            "inert",
	"LookupTrackerIp":                   "inert",
	"MaxAllocPeerRequestDataPerConn":    "inert",
	"MaxUnverifiedBytes":                "inert",
	"MetainfoSourcesConfig":             "inert",
	"MetainfoSourcesMerger":             "inert",
	"MinDialTimeout":                    "inert",
	"MinPeerExtensions":                 "inert",
	"NominalDialTimeout":                "inert",
	"NoUpload":                          "inert",
	"PeerID":                            "inert",
	"PeriodicallyAnnounceTorrentsToDht": "inert",
	"PieceHashersPerTorrent":            "inert",
	"PublicIp4":                         "inert",
	"PublicIp6":                         "inert",
	"Seed":                              "inert",
	"Slogger":                           "inert",
	"TorrentPeersHighWater":             "inert",
	"TorrentPeersLowWater":              "inert",
	"TotalHalfOpenConns":                "inert",
	"UploadRateLimiter":                 "inert",
	"UpnpID":                            "inert",
	"WebsocketTrackerHttpHeader":        "inert",
}

// TestEveryEgressFieldOfTheClientConfigIsClassified is the one test here that
// survives an upgrade.
//
// Every other assertion in this package checks a field somebody thought to look
// at. This one checks the FIELD SET: bump anacrolix and any new way out of the
// process fails the build until a person has said which of the four it is. That
// is the guard the three leaks this task closed did not have — DisableWebtorrent
// arrived in a release nobody read the changelog of, and nothing noticed for two
// phases.
//
// Same idiom as cmd/curator's TestEveryRegisterIsWiredIntoMain: reflect over the
// dependency rather than keep a list somebody has to remember to extend.
func TestEveryEgressFieldOfTheClientConfigIsClassified(t *testing.T) {
	var missing, unknown []string
	seen := map[string]bool{}

	var walk func(reflect.Type)
	walk = func(typ reflect.Type) {
		for i := range typ.NumField() {
			field := typ.Field(i)
			if !field.IsExported() {
				continue
			}
			seen[field.Name] = true
			if _, ok := egressClassification[field.Name]; !ok {
				missing = append(missing, field.Name)
			}
			if field.Anonymous && field.Type.Kind() == reflect.Struct {
				walk(field.Type)
			}
		}
	}
	walk(reflect.TypeOf(anacrolix.ClientConfig{}))

	for name := range egressClassification {
		if !seen[name] {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(unknown)

	if len(missing) > 0 {
		t.Errorf("anacrolix.ClientConfig has %d field(s) nobody has classified: %s\n"+
			"Read what each one does and add it to egressClassification as bound, disabled, nil or inert. "+
			"A field left out is a way out of this process that no test is watching.",
			len(missing), strings.Join(missing, ", "))
	}
	if len(unknown) > 0 {
		t.Errorf("egressClassification names %d field(s) anacrolix no longer has: %s\n"+
			"Delete them — a classification for a field that is gone is a line that reads as coverage and is not.",
			len(unknown), strings.Join(unknown, ", "))
	}

	for name, class := range egressClassification {
		switch class {
		case "bound", "disabled", "nil", "inert":
		default:
			t.Errorf("%s is classified %q, which is not one of bound, disabled, nil, inert", name, class)
		}
	}
}

// TestTheConfigLeavesNoTransportThatWouldReplaceTheBoundOne is the `nil` half of
// the classification above, asserted on the real production config rather than
// trusted to a comment.
//
// It is not "these fields are unset because we never set them". It is that
// setting either of them would void HTTPDialContext WITHOUT touching
// HTTPDialContext, so a future line that looks like it is adding a timeout or a
// user agent can silently move every webseed byte onto the host's stack.
func TestTheConfigLeavesNoTransportThatWouldReplaceTheBoundOne(t *testing.T) {
	cc := clientConfig(context.Background(), Config{
		DataDir: t.TempDir(), Category: "curator",
		Network: &resolver{answer: []string{"192.0.2.1"}}, Log: quiet(),
	}, t.TempDir())

	if cc.WebTransport != nil {
		t.Error("WebTransport is set: client.go:284-295 uses it INSTEAD of the transport carrying " +
			"HTTPDialContext, so webseeds and metainfo sources would leave on the host's stack")
	}
	if cc.MetainfoSourcesClient != nil {
		t.Error("MetainfoSourcesClient is set: client.go:296 uses it instead of the bound http.Client, " +
			"so metainfo source fetches would leave on the host's stack")
	}
	if cc.HTTPDialContext == nil {
		t.Error("HTTPDialContext is nil, so webseeds are on the host's stack")
	}
}
