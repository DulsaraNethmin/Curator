# T64 — `jellyfin.Provisioner`, which can write, and only where it is allowed to

**Owns:** `internal/jellyfin/provision.go`, its tests, and one paragraph of the package doc
**Depends on:** nothing — it is a Go package with a fake server in front of it, and the request
sequence is already measured

## Goal

curator sets up a Jellyfin it brought up: completes the startup wizard, creates a Movies library
pointing at the path curator writes to, and mints its own API key — **without ever being able to do
any of that to a Jellyfin somebody is already watching.**

The rule this reverses is in `internal/jellyfin`'s own package doc: *"no user or session endpoints …
nothing that writes, for the same reason `internal/qbit` cannot delete or pause a torrent: a method
that does not exist cannot be called by mistake against a media server the household is watching."*
That rule was right and it stays right for `Client`. This task adds a **second type** and a guard,
and records why in [D34](../decisions.md#d34--curator-provisions-a-jellyfin-it-brought-up-and-never-rewrites-one-somebody-is-already-watching).

## The request sequence, measured — implement this one, not a plausible one

Against a throwaway `jellyfin/jellyfin:10.10.7` container on 2026-08-15. Every status below was
observed, not inferred.

| # | Request | Answer |
|---|---|---|
| 0 | `GET /System/Info/Public` | `200` · `{"Version":"10.10.7","StartupWizardCompleted":false,"Id":…}` |
| 1 | `GET /Startup/Configuration` | `200` · `{"UICulture":"en-US","MetadataCountryCode":"US","PreferredMetadataLanguage":"en"}` |
| 2 | `POST /Startup/Configuration` | `204` — body is the same three fields |
| 3 | `GET /Startup/User` | `200` · `{"Name":"root"}` |
| 4 | `POST /Startup/User` · `{"Name":…,"Password":…}` | `204` |
| 5 | `POST /Startup/RemoteAccess` · `{"EnableRemoteAccess":true,"EnableAutomaticPortMapping":false}` | `204` |
| 6 | `POST /Users/AuthenticateByName` · `{"Username":…,"Pw":…}` | `200` · `AccessToken` (32 chars), `User.Id`, `ServerId` |
| 7 | `POST /Library/VirtualFolders?name=Movies&collectionType=movies&refreshLibrary=true` | `204` |
| 8 | `POST /Auth/Keys?app=curator` | `204`, **empty body** |
| 9 | `GET /Auth/Keys` | `200` · `Items[]` with `AccessToken`, `AppName`, `DateCreated` |
| 10 | `POST /Startup/Complete` | `204`; `/System/Info/Public` then reports `StartupWizardCompleted: true` |

Steps 1–5 need **no credential** while the wizard is incomplete. `/Auth/Keys` needs one even then —
unauthenticated it is `401`. Step 6 works **before** step 10, which is why the library and the key
are both in place before the wizard is closed: if anything fails, the server is still visibly
unfinished rather than half-configured and claiming otherwise.

Step 7's body is `{"LibraryOptions":{"PathInfos":[{"Path":"<curator's movies path>"}]}}` and
**nothing else** — see *Do not*.

Verified afterwards with the **minted** key rather than the user token: `POST /Library/Refresh` →
`204`, and `GET /Items?Recursive=true&IncludeItemTypes=Movie&Fields=ProviderIds&years=2026` returned
the film with `ProviderIds: {"Tmdb":"1083381","Imdb":"tt26657236"}` and `ProductionYear: 2026`. Those
are curator's only two existing Jellyfin calls, so the key this mints is proven against the code that
consumes it — **and it confirms [D32](../decisions.md#d32--the-jellyfin-link-is-keyed-on-the-tmdb-id-not-on-the-path)
holds on a library curator created itself.**

## Do

1. **`Provisioner` is its own type, in `provision.go`, and the setup flow is the only thing that
   constructs it.** `NewProvisioner(baseURL string, httpClient *http.Client) *Provisioner` — note it
   takes **no API key**, because it does not have one yet and that is the point. The importer and the
   poller keep `*Client`, which is unchanged and still cannot write.

2. **Every writing method calls the guard first, and the guard reads the server rather than a
   field.** `Status(ctx) (ServerStatus, error)` over `GET /System/Info/Public` returns at least
   `Version`, `ServerID` and `WizardCompleted`. Any method that touches `/Startup/*` re-reads it and
   returns `ErrAlreadyConfigured` when the wizard is complete. **Do not cache the answer from an
   earlier call**: the whole failure being defended against is a server that changed underneath a
   half-finished flow.

3. **Exactly two operations are permitted against a completed server**, and both are additive and
   both take an explicit caller opt-in rather than a bool that defaults true:
   `AddLibrary` and `MintKey`. That is T66's branch. Everything under `/Startup/` is refused
   unconditionally.

4. **`Authenticate(ctx, user, pass) (Session, error)`** sends the header Jellyfin requires:
   `Authorization: MediaBrowser Client="curator", Device="curator", DeviceId=<stable>, Version=<build>`.
   Return `ErrBadCredentials` for `401` and a **distinct** error for `400` — they mean opposite things
   (see *Do not*). `Session` carries the token, `UserID` and `ServerID`.

5. **`MintKey(ctx, session, app string) (string, error)` reads the key back.** `POST /Auth/Keys`
   answers `204` with no body, so: `GET /Auth/Keys`, filter on `AppName == app`, and if more than one
   matches take the newest `DateCreated` **and log that it happened**. If none matches, that is an
   error and not an empty string.

6. **`AddLibrary(ctx, session, name, path string) error`** posts `PathInfos` only, with
   `refreshLibrary=true`, then **verifies by re-reading** `GET /Library/VirtualFolders` and finding
   `path` in a folder's `Locations`. A `204` is not proof (see *Do not*).

7. **Every method returns a typed reason the UI can turn into instructions.** The degrade-to-
   instructions rule is a phase requirement, so the errors have to distinguish *unreachable*, *bad
   credentials*, *already configured*, and **unexpected shape** — the last is what a future Jellyfin
   produces, and it is the one that must reach the screen as "do these steps by hand" with the exact
   path to paste.

8. **Amend the package doc.** It currently says the package contains *"nothing that writes"*. That
   stops being true in this commit and a doc comment that contradicts the file below it is worse than
   no doc comment. Say that writing lives on one type, that only the setup flow constructs it, that
   it refuses a configured server, and point at D34.

## Do not

- **Put a write method on `Client`, or give `Provisioner` a `RefreshLibrary`.** The separation is the
  entire deliverable. If the poller can reach a method that reconfigures a media server, this task
  failed no matter how the tests read.
- **Trust Jellyfin to protect a configured server. It does not.** Measured: on a server reporting
  `StartupWizardCompleted: true`, `POST /Startup/User {"Name":"attacker","Password":"x"}` with a valid
  API key answered **`204`** and renamed the admin account and changed its password — same user `Id`,
  and the original credentials answered `401` afterwards. Unauthenticated the same call is `401`. So
  the only thing standing between a configured Jellyfin and a locked-out household is this guard.
- **Map every 4xx from `/Users/AuthenticateByName` to "wrong password".** Missing the
  `Authorization: MediaBrowser …` header is **`400`**; a genuinely wrong password is `401`. Telling
  someone their password is wrong when the header is missing is the worst available message, because
  it is the one thing they will retype for ever.
- **Send a full `LibraryOptions`.** Unspecified booleans deserialise to `false`, and the listing then
  reports `EnableInternetProviders: false` — which is a red herring: `options.xml` omits the field and
  `<TypeOptions />` is empty, meaning *defaults apply*, and metadata fetching ran anyway. Sending more
  fields to make that flag read `true` invents a configuration Jellyfin's own UI never produces, and
  it is how `ProviderIds.Tmdb` gets turned off by accident — which would break Open in Jellyfin for
  every film, silently, with the link still rendering.
- **Believe a `204` from `/Library/VirtualFolders`.** A folder created with `refreshLibrary=false`
  came back in the listing with **no `ItemId` and no `LibraryOptions` key at all** until a refresh
  materialised it.
- **Assume `POST /Auth/Keys` is idempotent.** Two posts with `app=curator` produced two keys both
  named `curator`, both with `Id: 0`. A retried provision otherwise litters the user's server with
  credentials it will never use and cannot tell apart.
- **Store the user's Jellyfin password.** It is used once, for one `AuthenticateByName`, and then it
  is the *key* that is persisted. curator has no reason to be able to log in as a human again.
- **Log the password, the user token or the minted key.** The key goes through phase 7's secret
  machinery, which means it is encrypted at rest and never read back across the API
  ([D28](../decisions.md#d28--settings-are-writable-secrets-are-encrypted-at-rest-and-write-only-across-the-api)).
- **Widen `Item`, or add a `Users` call because it is one line away.** The narrowness rule survives
  this task; it is amended, not repealed.

## Verify

Hermetic, against an `httptest.Server` — the shape every test in this package already uses:

- the full sequence in order against a fake answering exactly the statuses in the table above, ending
  with a non-empty key that came from `GET /Auth/Keys` rather than from the `POST`
- **the guard**: a fake reporting `StartupWizardCompleted: true` makes every `/Startup/*` method
  return `ErrAlreadyConfigured` **and issue no request at all** — asserted on the fake's recorded
  request log, not only on the error
- the guard is re-read per call: a fake that answers `false` then `true` refuses the second call
- `AddLibrary` and `MintKey` **do** work against that same completed server, and only with the
  explicit opt-in
- `AddLibrary` sends a body whose `LibraryOptions` has `PathInfos` and no other key — asserted on the
  decoded request body
- `AddLibrary` fails when the re-read listing does not contain the path, even though the `POST` was
  `204`
- `MintKey` with two matching `AppName` entries picks the newest and logs it; with none, errors
- `Authenticate` maps `401` → `ErrBadCredentials` and `400` → a different error, and the
  `Authorization` header is present and well-formed on the request the fake received
- an unexpected body shape at any step produces the *instructions* error, not a panic and not a
  generic 500 — the degrade path is a tested path

Then live, against a **throwaway container and never the Pi**:

```bash
docker run -d --name jf-throwaway -p 8097:8096 \
  -v "$PWD/jf-config":/config -v "$PWD/jf-cache":/cache -v "$PWD/media":/media \
  jellyfin/jellyfin:10.10.7
```

Cold start to a `200` on `/System/Info/Public` was **17.6 s**, so whatever polls for it needs to be
patient rather than fast. Provision it end to end, then prove the result with curator's own two
calls: `RefreshLibrary` returns nil and `FindMovie` returns the item for a film in that library.
`docker rm -f jf-throwaway` when done — and delete the config volume, or the next run starts with the
wizard already complete and silently tests the wrong branch.
