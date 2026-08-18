# T79 — the Download button, which has never worked in a browser

**Owns:** the one line that decides whether a release can be dispatched from the UI
**Found by:** [T53](T53-run-alongside.md), on the Pi, by a person clicking Download and being refused

## What was wrong

`web/components/releases.tsx` asked the settings endpoint whether downloads were possible by
looking for an integration named **`qbittorrent`**:

```js
const qbit = s.integrations.find((i) => i.name === 'qbittorrent');
setDownloadsConfigured(qbit?.configured ?? false);
```

The API has never emitted that name. It emits **`torrents`** (`cmd/curator/main.go:546`), because
[D22](../decisions.md#d22--the-torrent-engine-moves-inside-the-binary-and-qbittorrent-becomes-the-second-backend)
moved the engine into this binary and made qBittorrent the *second* backend rather than the only
one. Measured against the running Pi, the endpoint returns exactly:

```
['tmdb', 'torrents', 'vpn', 'minter', 'jellyfin', 'ffmpeg']      qbittorrent: absent
torrents.configured = True
```

So `find` returned `undefined`, `?? false` turned that into a hard `false`, and
`downloadsConfigured === false` was **true on every install, forever**. Every Download button was
disabled, and the banner told the user to set `QBIT_USER` and `QBIT_PASS` — an instruction that does
nothing at all on the embedded backend, where there are no credentials to set.

**The whole of D22 was unreachable from the browser.** curator could download; nobody could ask it
to without using the API directly, which is how T53's film was dispatched.

## Why it survived this long

The `?? false` is the load-bearing mistake, not the stale name. A missing integration is a *changed
contract*, and it was read as *a configured answer meaning no*. That converts a rename into a
silently disabled product, with no error, no console warning and no failed request — the button is
simply grey, and grey looks deliberate.

Nothing tested it, either: T53's film went out over `POST /api/downloads`, and every earlier phase
predates the rename or exercised dispatch below the UI.

## Do

1. Key on **`torrents`**.
2. **Fail open.** A missing integration sets `downloadsConfigured` to `null`, not `false` — unknown,
   button live, and a real dispatch returns a real error. A rename must never again disable the
   product silently.
3. Take the refusal sentence from the API's `detail` instead of hardcoding a qBittorrent one.
   `backendDetail` (`main.go:965`) already answers per backend, so the embedded user is never told
   to set a credential that does not exist.

## Verify

- with the embedded backend, the banner is absent and Download is live
- `TORRENT_BACKEND=qbittorrent` with no `QBIT_USER` still shows a banner, and it now reads
  "downloads are disabled: set QBIT_USER and QBIT_PASS" because the API said so
- a release dispatched from the browser reaches `POST /api/downloads`

## Not done here

The Pi runs the published `0.1.0` image, which carries the broken build. This fix reaches that box
only through a release ([T48](T48-release-pipeline.md)) — it is a UI change compiled into the
binary, so restarting the container cannot pick it up.
