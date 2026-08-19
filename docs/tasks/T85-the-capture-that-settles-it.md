# T85 — the capture that settles it

**Owns:** the one piece of evidence for D47 that does not come from curator
**Blocked on:** nothing. It needs an arm64 build on the Pi and about an hour.
**Status:** **not done.** Recorded so it is a task rather than a paragraph in a handoff.

## Why it is separate

[T82](T82-a-kill-switch-that-can-be-proved.md) closed four leaks,
[T83](T83-a-tunnel-that-is-watched.md) made the tunnel watched and
[T84](T84-a-kill-switch-you-can-see.md) put it on a screen. Every one of those is verified by a test
or by reading a dependency's source, and the screen is curator's own reading of curator.
[D27](../decisions.md#d27--the-vpn-is-mandatory-and-curator-owns-the-socket)'s promise is about
packets, and nothing here has looked at a packet.

`docs/progress.md`'s "kill the tunnel mid-download and confirm traffic stops" has been carried
unrun since phase 6. This is that check, plus the one the four leaks made necessary.

## What has to happen

1. **Cross-compile and deploy beside the pinned 0.2.0.** `GOOS=linux GOARCH=arm64 go build ./...`,
   scp, run on its **own `PORT`** with its own data directory and database. The pinned 0.2.0 keeps
   running. **Jellyfin at :8096 must not be touched**, and nothing is `pkill`ed broadly.
2. **`sudo tcpdump` on the real interface during a download.** Nothing but encrypted WireGuard to the
   endpoint. That is the only evidence that settles "not a single byte", and it is the only way to
   catch a fifth leak nobody has thought of — the negative-path test can only see what it was told to
   look at.
3. **Kill the peer mid-download and watch the bytes stop**, measured in bytes rather than log lines.
4. **Confirm the badge goes `stale` within 180 s**, downloads are held with the tunnel named as the
   reason, and they resume when it comes back **without losing progress**.

## What it would be evidence against

A `ws://` tracker in a real magnet. A DNS query on the host resolver from a path nobody classified.
An SSDP packet. Anything at all leaving on `eth0`/`wlan0` that is not WireGuard to the endpoint.

## Traps, from the environment

- The Pi runs a **pinned 0.2.0 with no updater**, and
  [D45](../decisions.md#d45--a-mandatory-value-belongs-to-the-service-that-needs-it-not-to-the-file-that-describes-it)
  does not change that. This build runs beside it, not over it.
- **NordVPN issues one key per account** and a session is per *(server, client key)*. The Pi is on
  `187.15.101.96` and the laptop on `187.15.102.104` precisely so the two do not flap; a config for a
  third instance has to **choose** its endpoint rather than accept the recommended one.
- `tcpdump` on the Pi needs `sudo`, and the capture must be on the **real** interface — a capture of
  the netstack proves nothing.
