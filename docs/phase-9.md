# Phase 9 — One command, and a way to watch on the TV

The phase where curator stops being something only we can install.

**Done when** a stranger with Docker runs `docker compose up -d`, opens a browser, and gets from
nothing to watching a film **on a device that is not that browser** — with every hard part in
between owned by curator rather than by a support thread.

---

## Two gaps that are the same gap

curator can find a film, download it through its own tunnelled engine, file it, and play it in a
browser. **It cannot be installed by anyone but us, and it cannot be watched anywhere but a
browser.**

There is no Dockerfile, no compose file and no CI in this repository — checked, not assumed. The
`README.md` still says *"phase 1 of 6"*, *"thirteen containers become six"*, and describes
qBittorrent as a dependency, which stopped being true in phase 6.
[D28](decisions.md#d28--settings-are-writable-secrets-are-encrypted-at-rest-and-write-only-across-the-api)
states this phase's promise in as many words — *"a stranger runs one `docker run` and configures the
rest in a browser"* — and phase 7 built the machinery for it: writable settings, encrypted secrets,
env-wins precedence. It has been waiting for the phase that uses it.

The second half is playback. curator's own player ([`phase-8.md`](phase-8.md)) is the right answer in
front of a laptop and will never be the answer on an Apple TV, because curator is not going to ship
native apps. Jellyfin already has them. So the division is **curator acquires and manages; Jellyfin
plays on everything that is not a browser** — and the onboarding for that today is: install a second
server yourself, click through its wizard, find its API-keys page, paste a key back, and get two sets
of Docker mounts to agree. The last step is the one people fail silently: the paths differ, the
library scans, nothing appears, and no error is produced anywhere.

**The local library is a live demonstration of exactly that.** *Backrooms (2026)* is on this laptop,
curator streams it, and its Open in Jellyfin link lands on a Jellyfin **search** rather than the
film — because Jellyfin's library is the Pi's disk and the file is not on it. That is
[D32](decisions.md#d32--the-jellyfin-link-is-keyed-on-the-tmdb-id-not-on-the-path)'s designed
fallback working correctly, and it is the path problem this phase removes by construction.

---

## The reading this is built on — **confirmed 2026-08-15**

You said: *"when the main app starts, does it start jellyfin as well even if user has not selected
jellyfin? if yes it's not what i expect."*

So **Jellyfin never runs unbidden.** `docker compose up -d` brings up **curator alone**. Jellyfin
sits behind a compose profile and only exists once you have chosen it as your playback method.

That is the **opt-in profile**, and it is what everything below assumes. It was written here as a
reading to be overruled rather than buried in a task file, because you left the bundling question
unselected and said *"i think this is what i prefer. but i'm open for suggestion"*. **Asked again
before T63 could hard-code it, and chosen: the opt-in profile, with the pasted command accepted as
its cost.** T63 may now depend on it without asking again.

**The consequence, stated plainly: there is one pasted command in the flow.** You ruled out the
Docker socket when it turned out that `-v /var/run/docker.sock` is root on the host and
[D25](decisions.md#d25--authentication-is-optional-and-off-by-default) ships curator with
authentication off by default — an unauthenticated LAN-wide root shell. Without the socket, nothing
inside curator can start a container. So choosing Jellyfin shows you **one line** to run in the
terminal you already used to install curator:

```
docker compose --profile jellyfin up -d
```

Everything after that line — the wizard, the library, the paths, the API key, the URL the Apple TV
needs — is curator's. That trade is recorded in
[D34](decisions.md#d34--curator-provisions-a-jellyfin-it-brought-up-and-never-rewrites-one-somebody-is-already-watching).

**The same mechanism serves minter**, which is what quietly rescues T49 — see below.

---

## What this phase already owed, from the repo rather than invented

`docs/progress.md:28` declares it: **"One command — the image, the release pipeline, minter on
demand", T47–T51.** Three of those numbers are cited by other documents and keep their meanings:

| | |
|---|---|
| **T47** | the Dockerfile (`phase-6.md:306`), and re-measuring the engine's `0444` under `PUID`/`PGID` (`phase-6.md:131`) |
| **T49** | fetching minter on demand when 1337x is enabled — hangs off phase 7's `indexer_1337x` (`phase-7.md:214`, `phase-7.md:381`) |
| **T51** | **claimed twice**, and this document resolves it — see immediately below |

### T51 is the documents. The first-run wizard is T50. — **confirmed 2026-08-15**

`phase-6.md:24` says T51 corrects the stale documents; `phase-7.md:384` says T51 is the first-run
wizard. **T51 keeps the documents**, for a reason that is not a coin toss: phase 6 *deliberately left
them stale* in exchange for that promise — *"Correcting them here would mean touching them twice"* —
so the stale README, the dead `yts.mx` and `.env.example`'s LAN addresses exist **because** T51 was
reserved. Renumbering it would strand a commitment made in a shipped phase's document.

The first-run wizard takes **T50**, which was reserved for this phase by the plan and never given a
meaning. It displaces nothing: no document cites T50. That is cheaper than the next-free-number
convention would be here — [T55](tasks/T55-stall-reason.md)'s rule exists to avoid displacing a
number *others cite*, and T50 is cited by nobody.

**`phase-7.md:384` is now wrong**, and correcting it is one of the lines T51 owns. It is not fixed in
passing, for the reason phase 6 gave: touching those documents twice is how the list of stale ones
stops being finite.

**T48 is the release pipeline**, the third thing `progress.md:28` names and the only one with no
citation anywhere. T52–T54 stay reserved for phase 10.

---

## Tasks

| Task | Owns | Depends on | State |
|---|---|---|---|
| [T47](tasks/T47-image.md) the image | `Dockerfile`, `.dockerignore`, `scripts/build-ffmpeg.sh` | — | **specified** |
| [T48](tasks/T48-release-pipeline.md) the release pipeline | `.github/workflows/` | T47 | **specified** |
| [T49](tasks/T49-minter-on-demand.md) minter on demand | `internal/indexer/`, `internal/api/`, `web/app/settings/` | T63 | **specified** |
| [T50](tasks/T50-first-run.md) the first run | `web/app/` , `internal/api/settings.go` | T47, T63 | **specified** |
| [T51](tasks/T51-documents.md) the documents | `README.md`, `.env.example`, `docs/*` | T47, T63 | **specified** |
| [T62](tasks/T62-make-restart.md) `make restart` | `Makefile` | — | **specified**, carried in from the backlog |
| [T63](tasks/T63-compose.md) the compose bundle | `compose.yaml` | T47 | **specified** |
| [T64](tasks/T64-jellyfin-provisioner.md) `jellyfin.Provisioner` | `internal/jellyfin/provision.go` | — | **specified** |
| [T65](tasks/T65-playback-screen.md) the Playback screen | `web/app/settings/`, `internal/api/jellyfin.go` | T63, T64 | **specified** |
| [T66](tasks/T66-adopt-jellyfin.md) adopt an existing Jellyfin | `internal/jellyfin/provision.go`, `web/` | T64, T65 | **specified** |

T47 is the spine: T48, T63, T50 and T51 all wait on an image existing. **T64 depends on nothing** and
can be written first — it is a Go package with a fake server in front of it, and the request sequence
it needs is already measured below.

The new Jellyfin numbers start after T62 because T47–T51 are cited elsewhere and T52–T54 belong to
phase 10. That is [T55](tasks/T55-stall-reason.md)'s convention stated outright: *"a task found
afterwards takes the next free number rather than displacing one other documents cite."*

**T62 is folded in rather than left in the backlog.** Its own file says *"queued for the next
phase"*, this is that phase, and it is the same subject: phase 9 is where how-you-run-this stops
living in somebody's memory. It is dev-loop plumbing rather than product, and it is listed here so
that fact is a decision instead of an omission.

---

## The user journey this specifies

1. `docker compose up -d` → **curator only**. No Jellyfin, no second container, no third.
2. Settings → **Playback**: *how do you want to watch?*
   - **In this browser** — phase 8's player. Nothing further runs, nothing further is asked. Done,
     and it stays a complete answer.
   - **On my TV, phone, Apple TV** — needs Jellyfin.
3. Choosing Jellyfin shows **one command** to paste. curator polls `http://jellyfin:8096` for it
   appearing. Measured cold start: **17.6 s**.
4. curator sees `StartupWizardCompleted: false` and offers **Set up Jellyfin**. You pick a username
   and password **in curator's UI**.
5. curator completes the wizard over Jellyfin's startup API: locale, the admin user, and a Movies
   library pointing at **the path curator already knows it writes to**. That is the whole point, and
   it is the step that cannot be got wrong when both services mount one volume.
6. curator authenticates as that user, **mints its own API key**, stores it encrypted through phase
   7's existing secret machinery, and fills in `jellyfin_url` and `jellyfin_public_url`.
7. You open Jellyfin on the Apple TV, sign in with the credentials you just chose, and the films are
   there — and stay there, because curator already refreshes the library on every import
   ([D15](decisions.md#d15--the-jellyfin-refresh-is-best-effort-and-its-key-is-optional)).

**Adopting an existing Jellyfin** — a NAS install, or the Pi at cutover — is the same screen and a
different branch (T66). The wizard is already complete, so curator **never reconfigures it**: it asks
for credentials once, mints a key, and *offers*, never forces, to add a library if none covers
curator's path.

---

## What Jellyfin 10.10.7 actually answered

Measured on 2026-08-15 against a **throwaway** `jellyfin/jellyfin:10.10.7` container on 8097, from
`docker run` to a working API key. **Nothing on the Pi was touched.** A task file carrying a guessed
API is worse than one that says the API was not checked, so this is the sequence T64 implements
rather than a plausible one.

| Step | Request | Answered |
|---|---|---|
| 0 | `GET /System/Info/Public` | `200`, `StartupWizardCompleted: false`, `Version: "10.10.7"` |
| 1 | `GET /Startup/Configuration` | `200` `{"UICulture":"en-US","MetadataCountryCode":"US","PreferredMetadataLanguage":"en"}` |
| 2 | `POST /Startup/Configuration` | `204` |
| 3 | `GET /Startup/User` | `200` `{"Name":"root"}` — the default, before anything is set |
| 4 | `POST /Startup/User` `{"Name","Password"}` | `204` |
| 5 | `POST /Startup/RemoteAccess` | `204` |
| 6 | `POST /Users/AuthenticateByName` | `200`, `AccessToken` (32 chars), `User.Id`, `ServerId` |
| 7 | `POST /Library/VirtualFolders?name=Movies&collectionType=movies&refreshLibrary=true` | `204` |
| 8 | `POST /Auth/Keys?app=curator` | `204` **with no body** |
| 9 | `GET /Auth/Keys` | `200`, the key in `Items[].AccessToken`, matched on `AppName` |
| 10 | `POST /Startup/Complete` | `204`, and `/System/Info/Public` flips to `true` |

Steps 1–5 need **no credential at all** while the wizard is incomplete; `/Auth/Keys` needs one even
then (`401` unauthenticated). Authenticating at step 6 works **before** `/Startup/Complete`, which is
why the library and the key can both be in place before the wizard is closed.

Then, with the **minted key** rather than the user token: `POST /Library/Refresh` → `204`, and
`GET /Items?…&years=2026` → the film. Those are curator's only two existing Jellyfin calls, so the
key T64 mints is proven against the code that will use it.

### The finding that matters most: D32's key survives provisioning

A library created entirely through the API — `LibraryOptions` carrying **only** `PathInfos` — scanned
and came back with:

```json
{"Name":"Backrooms","ProductionYear":2026,
 "Path":"/media/movies/Backrooms (2026)/Backrooms (2026).mp4",
 "ProviderIds":{"Tmdb":"1083381","Imdb":"tt26657236"}}
```

`Tmdb: 1083381` is the id curator holds for that film.
[D32](decisions.md#d32--the-jellyfin-link-is-keyed-on-the-tmdb-id-not-on-the-path) keys Open in
Jellyfin on exactly that field, and `years=` narrowing depends on `ProductionYear` agreeing with
TMDB's release year — both confirmed on a library curator provisioned itself, not on one a human
configured. **The deep link works on a bundle nobody has touched by hand**, which is the property the
whole phase is selling and the one that was worth a container to check.

---

## The shape

```
Dockerfile               FROM scratch, multi-arch, the static ffmpeg beside the binary     T47
.dockerignore            so the build context is not node_modules and a 2 GB library       T47
.github/workflows/       check on push; build and push on a tag                            T48
compose.yaml             curator; jellyfin and minter behind profiles; one media volume    T63
internal/jellyfin/
  client.go              unchanged — still read-only, still cannot write
  provision.go           Provisioner: the startup API, the library, the key                T64
internal/api/jellyfin.go probe, provision, adopt — the three the Playback screen calls      T65, T66
web/app/settings/        the Playback screen and the one command                            T65, T66
```

**`provision.go` in `internal/jellyfin`, not a new package.** It is the same server, the same base
URL and the same token header; a second package would duplicate all three and then disagree about
one of them. The separation that matters is the **type**, not the directory —
[D34](decisions.md#d34--curator-provisions-a-jellyfin-it-brought-up-and-never-rewrites-one-somebody-is-already-watching)
and the traps below.

**`internal/api/jellyfin.go`, not more handlers in `settings.go`.** Provisioning is not a setting
write: it is three calls to another server with a failure mode of its own, and it ends by writing
settings through the machinery phase 7 already built rather than beside it.

---

## What must not change

- **No Docker socket, anywhere, for any reason.** Not for Jellyfin, not for minter, not "just to
  check if it is running". It is root on the host handed to a service that ships with authentication
  off ([D25](decisions.md#d25--authentication-is-optional-and-off-by-default)). This is the one
  constraint in the phase with no escape hatch, and it is why there is a pasted command.
- **`internal/jellyfin.Client` does not gain a write method.** The narrowness in its package doc is
  the design — *"a method that does not exist cannot be called by mistake against a media server the
  household is watching"* — and the importer and the poller keep exactly the client they have. What
  changes is that a **second type** exists, constructible only by the setup flow.
- **`CGO_ENABLED=0`, explicitly, in the Dockerfile.** Phase 6's finding 1: with cgo on, the engine
  pulls `go-libutp`, `go-llsqlite/crawshaw` and `crawshaw/c`, so the image gets a cgo uTP *and a
  second SQLite*. Cross-compiling disables cgo by itself, which is why this is invisible everywhere
  except in the image ([D4](decisions.md#d4--pure-go-sqlite)).
- **The stream endpoint's own MIME table stays.** [T43](tasks/T43-stream.md) carries four entries
  because `FROM scratch` has none of the four files Go reads for MIME types, so every
  `mime.TypeByExtension` answer becomes `""`, `ServeContent` sniffs, and **sniffing an MKV gives
  `video/webm`** — Matroska and WebM share the EBML magic. This is a phase 9 bug that was fixed in
  phase 8 before it could happen. Deleting that table as redundant is how it comes back.
- **[D8](decisions.md#d8--import-by-hardlink)'s hardlink.** The importer hardlinks, so
  `DOWNLOADS_DIR` and `LIBRARY_MOVIES` must be on one filesystem. The compose file makes that
  structural rather than documented — one named volume, two directories inside it.
- **Authentication stays off by default.** A bundle that turns it on to look responsible would lock a
  household out of a library it has not finished installing.
- **curator's own player is not demoted.** Choosing Jellyfin *adds* a way to watch. "In this browser"
  runs no second container and remains a complete answer.
- **Nothing on the Pi.** Phase 10, after T52 backs up the \*arr configs.

---

## The traps, named before anyone hits them

**Jellyfin will let a valid API key rewrite the admin of a server the household is watching.**
Measured, and it is the reason D34 has teeth. On a Jellyfin reporting
`StartupWizardCompleted: true` — fully configured, in use — a single call:

```
POST /Startup/User   {"Name":"attacker","Password":"x"}   with a valid X-Emby-Token   →  204
```

renamed the admin account and changed its password. Same user `Id`, and the original credentials
answered `401` afterwards. Unauthenticated the same call is `401`, so the only thing between a
configured server and a locked-out household **is the caller's own restraint**. The guard cannot be
"Jellyfin will refuse", because Jellyfin does not: `Provisioner` reads `/System/Info/Public` first
and refuses every startup endpoint when the wizard is complete.

**`POST /Auth/Keys` does not return the key, and it is not idempotent.** It answers `204` with an
empty body; the key has to be read back from `GET /Auth/Keys` and matched on `AppName`. Posting twice
with the same `app=curator` produced **two** keys both named `curator`, both with `Id: 0` — so a
retried provision silently accumulates credentials on the user's server. Check before minting, and
if more than one matches, take the newest by `DateCreated` and say so in the log.

**`POST /Users/AuthenticateByName` without an `Authorization` header is `400`, not `401`.** It needs
`Authorization: MediaBrowser Client="…", Device="…", DeviceId="…", Version="…"`. A provisioner that
maps every 4xx to "wrong password" will tell the user their password is wrong when the header is
missing, which is the worst possible message: it is the one thing they will retype forever.

**`GET /Library/VirtualFolders` is not proof the library exists.** A folder created with
`refreshLibrary=false` came back in the listing with **no `ItemId` and no `LibraryOptions` key at
all** until a refresh materialised it. Create with `refreshLibrary=true` and verify by re-reading
`Locations`, not by the call returning `204`.

**`EnableInternetProviders: false` in that listing is a red herring — do not "fix" it.** The
response says `false` and `options.xml` on disk omits the field entirely with `<TypeOptions />`
empty, which means *defaults apply*. Metadata fetching ran anyway, which is how `ProviderIds.Tmdb`
got written. Sending a fuller `LibraryOptions` to make the flag read `true` is inventing a
configuration the Jellyfin UI never produces, in an object whose unspecified booleans deserialise to
`false`. **Send `PathInfos` and nothing else.**

**`JELLYFIN_PUBLIC_URL` is where this quietly fails.** Inside compose curator reaches Jellyfin at
`http://jellyfin:8096`; the Apple TV needs a LAN address, and **curator cannot learn the host's LAN
IP from inside a container**. The answer is already in the browser — it reached curator over that
address — so `window.location.hostname` plus Jellyfin's published port is the default, offered and
editable, never guessed silently. **The compose file must publish `8096` to the host** or the Apple
TV cannot reach it at all, which is a one-line omission that produces a bug report about Jellyfin.

**The Jellyfin startup endpoints are not a stable documented contract.** They are what the setup
wizard happens to call in the version we pinned. So the image tag is pinned to **10.10.7**, and every
step **degrades to instructions**: when the API answers something unexpected, show the manual steps
with the exact path to paste rather than failing. A provisioning flow that breaks on the next
Jellyfin release and has no fallback is a support burden this project cannot carry.

**A profile is invisible, not just stopped.** `docker compose config --services` with no profile
lists **only** `curator` — measured. That is the property being sold, and it also means a typo in a
profile name produces a service that never starts and never errors. `--profile jellyfin up -d` starts
Jellyfin and **leaves curator running** rather than recreating it, so the pasted command is additive
and safe to run against a live install.

**`1337x` is a legal compose profile name** — it parses, checked, because profile names must start
alphanumeric and a leading digit looked like a plausible way to lose an afternoon.

**A long-running dev server does not pick up a merge**, which is [T62](tasks/T62-make-restart.md)'s
whole reason for existing and cost most of a session once already: 8090 served a `go run` binary from
two days earlier while the merged code was already correct. `ps -o lstart -p <pid>` against the merge
time settles it.

**Copying the database means copying the WAL.** `curator.db-wal` was 951 KB and newer than
`curator.db`; copying the `.db` alone runs against a stale snapshot that produces plausible answers
for the wrong reason. Any backup or volume-migration line in T51 has to say so.

---

## What T49 becomes, now that the socket is gone

T49 was conceived as *curator fetches minter when you enable 1337x*, and `phase-7.md:381` says the
Docker-socket cost that carries is **D23**'s to record. **The socket is ruled out, so the original
design is dead** — curator cannot pull an image or start a container, and nothing in this phase gives
it a way to.

The rescue is that the mechanism already exists: **minter goes behind a compose profile, exactly like
Jellyfin.** Enabling 1337x in Settings shows one line — `docker compose --profile 1337x up -d` —
curator probes `http://minter:8191`, and the indexer reports itself unconfigured until it answers.
That is the same shape, the same failure mode, and one fewer concept.

**D23 stays reserved and unwritten**, deliberately. Its subject was the cost of a socket curator no
longer takes, and the reasoning that killed it is recorded where it is now load-bearing, in
[D34](decisions.md#d34--curator-provisions-a-jellyfin-it-brought-up-and-never-rewrites-one-somebody-is-already-watching)'s
alternatives.

---

## Configuration this phase adds

| Variable | Default | Means |
|---|---|---|
| `PUID` / `PGID` | `1000` | the uid/gid the process runs as, applied by the entrypoint before curator starts |
| `PLAYBACK_TARGET` | empty | `browser` or `jellyfin` — what was chosen on the Playback screen, so it stops asking |

**`PUID`/`PGID` are deliberately not in the settings registry.**
[D28](decisions.md#d28--settings-are-writable-secrets-are-encrypted-at-rest-and-write-only-across-the-api)'s
rule decides it in one line — *anything needed in order to reach the settings screen is not settable
from the settings screen* — and these are read by the entrypoint before the process exists.

`PLAYBACK_TARGET` is the only new registry entry, and it is a record of an answer rather than a
switch that changes behaviour: an empty value means the question has not been asked yet, which is
what makes the Playback screen the first-run destination and not a nag. Everything else this phase
configures — `jellyfin_url`, `jellyfin_public_url`, `jellyfin_api_key`, `indexer_1337x`,
`minter_url` — already exists in phase 7's registry, which is the point of having built it.

---

## Verification

Per commit, as ever:

```bash
make check      # npm export, go build, go vet, go test -race, arm64 cross-compile
```

The doc-level claims were checked before any code exists, which is the part usually skipped:

- **the startup API**, against a throwaway 10.10.7 container — the table above, request by request
- **the path claim, by construction**: one named volume mounted by both services, `LIBRARY_MOVIES`
  inside curator equal to the library path handed to Jellyfin. Measured in a two-service compose
  project: a file hardlinked from `/media/downloads` to `/media/movies` had **inode 343760 and link
  count 2**, and the *other* container saw the same inode at the same path. That is phase 4's own
  proof re-run inside the bundle.
- **the profile**, measured: no profile lists and starts curator alone; `--profile jellyfin` adds
  Jellyfin without recreating curator
- `make status` derives the phase table and the task list from the files, so this document is wrong
  if its table and `docs/tasks/` disagree — run it after any edit

Then, once built, the phase's own definition of done, on a machine that has never seen this project:
one `docker compose up -d`, choose Jellyfin, paste one line, pick a password, and **sign in on a
device that is not this laptop**.

---

## Out of scope

- **The cutover.** Phase 10, and T52's \*arr config backup comes first. Nothing on the Pi changes
  here, and `internal/jellyfin/live_test.go`'s `TestLiveRefreshLibrary` stays `t.Skip`'d.
- **A Docker socket, a privileged container, `NET_ADMIN`.** None of them, none of the time.
- **Managing Jellyfin after setup** — users, transcoding settings, hardware acceleration, playback
  control. curator sets one up and then goes back to being read-only against it.
- **Kubernetes, Helm, a systemd unit, a `.deb`.** One compose file. Anyone who wants another shape
  has a pinned image to build it from, which is T47's actual deliverable.
- **TLS, a reverse proxy, a domain.** The bundle is a LAN product
  ([D25](decisions.md#d25--authentication-is-optional-and-off-by-default) is explicit that there is
  no TLS and says so where the password is set).
- **Migrating an existing install's database into a volume.** Nobody has one but us.
- **Sonarr's half — television.** Still not this product.
