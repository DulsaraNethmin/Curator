# T49 — minter on demand, behind a profile

**Owns:** the 1337x branch of `internal/indexer/`, a probe in `internal/api/`, and the Indexers group
in `web/app/settings/`
**Depends on:** [T63](T63-compose.md)

## Goal

Turning on 1337x in Settings tells you exactly what to run and then confirms it worked — instead of
an indexer that silently returns nothing because a companion service nobody mentioned is not running.

## What this task was, and why it is not that any more

`phase-7.md:381` reserved T49 as *"fetching minter when 1337x is enabled"*, and said the
**Docker-socket cost it carries is D23's to record**. That design assumed curator could start a
container.

**It cannot, and it will not.** The socket is root on the host, handed to a service that ships with
authentication off by default
([D25](../decisions.md#d25--authentication-is-optional-and-off-by-default)), and it was ruled out for
Jellyfin before it was ruled out here
([D34](../decisions.md#d34--curator-provisions-a-jellyfin-it-brought-up-and-never-rewrites-one-somebody-is-already-watching)).

So minter takes the same shape Jellyfin takes: **a compose profile and one pasted line.** That is one
fewer concept in the product rather than a workaround, and it is why this task is small.
`indexer_1337x` — the setting phase 7 stored for exactly this — is still what it hangs off.

**D23 stays reserved and unwritten.** Its subject was the cost of a socket curator no longer takes.

## Do

1. **The setting shows the command when it is switched on.**

   ```
   docker compose --profile 1337x up -d
   ```

   `1337x` is a legal compose profile name — checked, because a leading digit looked like a plausible
   way to lose an afternoon.

2. **Probe `MINTER_URL` and report reachable or not, in the Settings screen, beside the toggle.** The
   toggle is what the user controls; whether minter is answering is a fact, and it belongs next to it
   rather than in the log after a search returns nothing.

3. **A 1337x search with no minter fails as *unconfigured*, not as *no results*.** This is the whole
   value of the task. The aggregator already merges what it gets, so an indexer that is off must be
   distinguishable from an indexer that found nothing — otherwise the correct answer and the broken
   one look identical.

4. **`http://minter:8191` inside compose, `http://127.0.0.1:8191` outside.** Not `localhost`: minter
   binds IPv4 only, so `localhost` resolves to `::1` first and the connection fails. That is
   `MINTER_URL`'s default and it is documented in CLAUDE.md because it has already cost time once.

5. **Pin minter's image in the compose file**, like everything else there.

## Do not

- **Mount the Docker socket, pull an image, or shell out to `docker`.** The whole point.
- **Turn 1337x on by default.** It needs a second container; YTS and TPB are plain JSON and need
  nothing.
- **Try to be clever and skip the browser.** Cloudflare binds `cf_clearance` to the exit IP, the
  User-Agent *and* the TLS fingerprint, and no uTLS profile reproduces minter's patched Firefox 151 —
  measured, all three fingerprints differ
  ([D2](../decisions.md#d2--fetch-pages-through-the-browser-do-not-reuse-cookies)).
- **Fail a whole search because one indexer is unreachable.** The aggregator's existing behaviour is
  right; this only makes the reason visible.
- **Report a stale probe.** A minter that was up when the page loaded and is down now should not be
  shown as up because the answer was cached.

## Verify

- toggling `indexer_1337x` on with no minter running: the setting saves, the screen says minter is
  not reachable, and shows the command
- `docker compose --profile 1337x up -d`, then the same screen reports it reachable with no reload
  beyond the probe's own interval
- a 1337x search with minter down reports **unconfigured** and does not merge an empty result set
  into a successful-looking search
- YTS and TPB searches are unaffected in both states
- a real 1337x search through a running minter still returns releases — the existing live test, which
  this task must not break
