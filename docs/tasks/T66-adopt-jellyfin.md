# T66 — adopt an existing Jellyfin, without reconfiguring it

**Owns:** the adopt branch of `internal/jellyfin/provision.go` and of the Playback screen
**Depends on:** [T64](T64-jellyfin-provisioner.md), [T65](T65-playback-screen.md)

## Goal

Somebody who already runs Jellyfin — on a NAS, on another box, or on the Pi at the phase 10 cutover —
connects curator to it in the same screen, and **curator changes nothing about how that server is set
up** except, with explicit permission, adding a library.

This is the branch that will be taken on the Pi, where the household is watching, so it is the branch
where being wrong is expensive.

## Do

1. **The branch is chosen by the server, not by the user.** `GET /System/Info/Public` reporting
   `StartupWizardCompleted: true` means adopt. No radio button asking "is this a new Jellyfin?" —
   that is a question the software can answer and the user can get wrong.

2. **Ask for the URL first, and probe it before asking for anything else.** An unreachable address is
   a typo or a firewall, and finding that out after someone has typed a password is a worse
   experience for no reason.

3. **Credentials once, then a key.** `Authenticate`, then `MintKey` with `app=curator`, then the
   password is discarded. Report the two failures differently: `401` is a wrong password, `400` is
   curator's own missing `Authorization: MediaBrowser …` header and must never be shown as "wrong
   password".

4. **Read the libraries and say what was found.** `GET /Library/VirtualFolders` returns `Locations`
   per folder. Compare against the path curator writes to and show the user which of three cases they
   are in, in a sentence rather than a diagnostic:

   - **a library already covers it** — nothing to do, and say so;
   - **no library covers it** — *offer* to add one, with the exact path, and a clear statement that
     this adds a library and changes nothing else;
   - **curator's path does not exist on that server at all** — the normal case for a Jellyfin on
     another machine. curator **cannot fix this**, and must say so plainly instead of adding a
     library pointing at a path that server cannot see. The films will not appear, and the reason is
     that two machines do not share a disk.

5. **The path comparison is a hint, not a verdict, and the UI must say which.** Jellyfin reports its
   own mount — `/movies` — while curator holds its own —
   `/media/storage/media/movies`. [D32](../decisions.md#d32--the-jellyfin-link-is-keyed-on-the-tmdb-id-not-on-the-path)
   established that those two strings disagree on **every deployment where the services see the disk
   through different mounts, which is the normal one**. So a non-match is not evidence of a problem;
   it is evidence that curator cannot tell. Offer, explain, and let the person who knows their mounts
   decide.

6. **Adding a library is the one write, and it is opt-in per use.** T64's `AddLibrary` against a
   configured server, behind the explicit caller opt-in, never a default-true flag. `MintKey` is the
   other permitted operation and it adds a credential rather than changing one.

7. **`jellyfin_public_url` matters more here, not less.** An adopted server is often on a different
   host from curator, so the `window.location.hostname` default T65 uses is **wrong** in this branch:
   the browser reached *curator*, not Jellyfin. Default it from the URL the user just typed for
   `jellyfin_url` instead, and keep it editable.

8. **Prove the connection with curator's own calls before declaring success.** `FindMovie` for a film
   the library already holds, using the newly minted key. A key that authenticates but cannot read
   `/Items` is a working setup screen and a broken Open in Jellyfin link.

## Do not

- **Touch `/Startup/*`. Ever, on this branch.** T64's guard already refuses, and this is the branch
  where it earns its keep. Measured: on a server reporting `StartupWizardCompleted: true`,
  `POST /Startup/User` with a valid API key answered **`204`** and renamed the admin account and
  changed its password — the original credentials then answered `401`. Jellyfin does not protect
  itself here. curator does.
- **Change locale, metadata language, remote-access settings or any existing library.** Not to make
  them match curator's, not to "fix" anything. The user configured that server.
- **Delete or edit a library that overlaps curator's path.** Overlapping is legal and common —
  [D32](../decisions.md#d32--the-jellyfin-link-is-keyed-on-the-tmdb-id-not-on-the-path) recorded
  `Iron Man` appearing twice in the real library under one TMDB id, once under `/movies` and once
  under `/media/downloads/complete`, and the first match winning is a documented, deliberate
  behaviour rather than a symptom.
- **Create a user.** Adoption uses the admin account the person already has.
- **Add a library without being asked**, and do not pre-tick the box.
- **Assume one library.** A real server has Movies, Shows, Music and a folder somebody made in 2019.
- **Run this against the Pi.** Read-only until phase 10, after T52 backs up the \*arr configs.
  `internal/jellyfin/live_test.go`'s `TestLiveRefreshLibrary` stays `t.Skip`'d.

## Verify

Hermetic, against a fake reporting `StartupWizardCompleted: true`:

- every `/Startup/*` method returns `ErrAlreadyConfigured` and **issues no request** — asserted on the
  fake's request log
- the three path cases each render their own sentence, and only the middle one offers a button
- declining the offer leaves the server's `VirtualFolders` byte-identical
- accepting sends `PathInfos` only, and verifies by re-reading `Locations` rather than trusting the
  `204`
- `400` and `401` from `AuthenticateByName` produce different messages
- `jellyfin_public_url` defaults from the typed URL, not from `window.location.hostname`

Then live, against a **throwaway container whose wizard has already been completed** — which is one
`docker run` plus T64's own sequence, and is the only honest way to test this branch without touching
the Pi:

- adopt it, mint a key, and confirm `FindMovie` answers with that key
- confirm the admin credentials that existed before adoption **still work afterwards**. That is the
  single check this task exists for.
