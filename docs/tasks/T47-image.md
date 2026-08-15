# T47 — the image, `FROM scratch` and multi-arch

**Owns:** `Dockerfile`, `.dockerignore`, and whatever `scripts/build-ffmpeg.sh` needs to be callable
from a build stage
**Depends on:** nothing — but everything else in phase 9 waits on it

## Goal

`docker run ghcr.io/dulsaranethmin/curator` serves the UI on 8090 on both amd64 and arm64, with the
remux available, and nothing in the image that is not curator.

There is **no Dockerfile in this repository** — checked. This builds one from nothing, and phase 6
left it a bill to pay: *"The Dockerfile must set `CGO_ENABLED=0` explicitly"* (`phase-6.md:113`), and
*"T47 has to re-measure it again under `PUID`/`PGID`"* about the engine's `0444`
(`phase-6.md:131`).

## Do

1. **`CGO_ENABLED=0`, written out, in the build stage.** Not inherited, not implied. With cgo on the
   engine pulls `go-libutp`, `go-llsqlite/crawshaw` and `crawshaw/c`, so the image would ship a cgo
   uTP **and a second SQLite** beside the pure-Go one
   ([D4](../decisions.md#d4--pure-go-sqlite)). Cross-compiling turns cgo off by itself, which is
   exactly why `make check`'s arm64 step has never caught this and why it is invisible until here.

2. **Three stages: the UI, the binary, then `FROM scratch`.** The UI stage runs
   `npm --prefix web ci && npm --prefix web run build` and hands `internal/web/dist/` to the Go stage,
   because the binary embeds it and the order is load-bearing
   ([D16](../decisions.md#d16--the-ui-is-embedded-with-all-and-a-committed-placeholder-keeps-go-build-honest)).
   A build that skips the export produces a binary that says so in the log and serves the API
   perfectly — which is easy to miss if you are only curling.

3. **The final stage carries four things and no more:** the binary, the static ffmpeg, CA
   certificates, and `/etc/passwd`-equivalent enough for a numeric uid to be legal. TMDB, the
   indexers and Jellyfin are all HTTPS, so a missing CA bundle is a working image that cannot fetch
   metadata.

4. **`TARGETPLATFORM`, and the ffmpeg that matches it.** The static ffmpeg is **2,232,152 bytes**
   stripped for linux/arm64, ffmpeg 8.1.1, built by `scripts/build-ffmpeg.sh`. An amd64 image needs
   an amd64 one; shipping the arm64 binary to both is a remux that fails with `exec format error` on
   half the installs. Build it per platform in its own stage, or fetch a pinned prebuilt — decide and
   say which in the commit message.

5. **`PUID`/`PGID`, applied by an entrypoint before curator starts.** They are **not** settings:
   [D28](../decisions.md#d28--settings-are-writable-secrets-are-encrypted-at-rest-and-write-only-across-the-api)'s
   rule is that anything needed in order to reach the settings screen is not settable from it, and
   these are read before the process exists. `1000:1000` default.

   **`FROM scratch` has no shell, so there is nowhere to run `chown` at start-up.** Either the final
   stage is not `scratch` (a distroless or busybox base, and the image budget absorbs it), or curator
   is started with `--user` and the volume permissions are the operator's problem, documented. **Pick
   one before writing the Dockerfile** — it decides the base image, and discovering it afterwards
   means rewriting the whole file.

6. **Re-measure the `0444`.** Phase 6 measured the engine chmodding finished files to `0444`, and the
   hardlinked library copy inheriting it because a hardlink is a second name for one inode. That was
   measured on a laptop as one user. Under `PUID`/`PGID` with a volume owned by somebody else, the
   question is whether Jellyfin — a *different* container running as a *different* uid — can read it.
   That is the failure this whole phase exists to prevent, so it is measured rather than reasoned
   about.

7. **A `.dockerignore` that is not an afterthought.** `node_modules`, `.next`, `.git`, `*.db`,
   `*.db-wal`, `testdata/`, and the local library. Without it the build context is a git history and
   possibly a 2 GB film.

8. **`HEALTHCHECK` against `/healthz`**, which exists (`cmd/curator/main.go:294`) and is deliberately
   cheap — no database, no disk. Compose needs it for `depends_on: condition: service_healthy`, and
   T65's polling for Jellyfin is a different mechanism that should not be confused with it.

## Do not

- **Ship `latest` as the only tag**, or leave the base images unpinned. The phase's promise is that
  the same command works next month.
- **`go build` without `-trimpath` and `-ldflags "-s -w"`.** Phase 6 measured `-s -w` at 17.62 MB
  against 25.22 MB unstripped for arm64, and the image budget is built on the smaller number.
- **Run as root.** The default is a numeric uid, and a root default in a container that mounts the
  user's media is the posture this project spent D25 and D27 avoiding.
- **Add a shell "for debugging", a package manager, or `curl`.** Each is a permanent attack surface
  bought against one afternoon's convenience. `docker cp` and a second container reach the same
  volume.
- **Bake a `.env`, a database, or `LIBRARY_MOVIES=./testdata/…` into the image.** The fixture default
  is right for a fresh clone and wrong for a container: it makes a stranger's first scan report 29
  films they do not have.
- **Delete [T43](T43-stream.md)'s four-entry MIME table.** It looks redundant beside a real
  filesystem and it is not: `FROM scratch` has none of the four files Go reads for MIME types, so
  every `mime.TypeByExtension` answer becomes `""`, `ServeContent` sniffs, and **sniffing an MKV
  gives `video/webm`**. This is the image where that bug would have appeared.

## Verify

- `docker buildx build --platform linux/amd64,linux/arm64` succeeds, and `docker image inspect`
  reports both
- the built image **contains no cgo**: `go version -m` on the extracted binary, and the build log
  showing `CGO_ENABLED=0`
- `docker run -p 8090:8090` serves the UI at `/` and `200` at `/healthz` — the UI, so the embed
  actually happened rather than the placeholder shipping
- **an HTTPS fetch works from inside the image** — a TMDB search — which is the CA bundle proven
  rather than assumed
- the remux path runs: `POST /api/movies/{id}/playback` returns a `remux_url`, and it produces bytes
- `.mkv` streams as `video/x-matroska` **from inside the image**, not `video/webm`
- a file written by the engine under `PUID=1000` is readable by a second container running as a
  different uid against the same volume — the `0444` question, answered where it matters
- image size recorded in the commit message, against phase 6's 17.62 MB binary and the 2.2 MB ffmpeg,
  so the next person knows what the base cost
