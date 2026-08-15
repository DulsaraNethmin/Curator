# T59 — curator refuses to fetch a film it already has

**Owns:** one check in `handleDispatch` (`internal/api/downloads.go`), and the `ApiError` doc
comment in `web/lib/api.ts`
**Depends on:** `store.LibraryByTMDBID`, which already exists and already answers this

## Goal

`POST /api/downloads` stops being a supported way to download a film that is already in the library.

Nothing at any layer consults the library today. `GET /api/search` validates a title and a year;
`handleDispatch` validates `release_id`, `title` and `year >= 0`. Re-downloading a film you already
have is a fully supported path, and the only thing stopping it is that nobody has tried.

## The gate is `imported`, and nothing else

A film **mid-download is not "already downloaded"**, and leaving it ungated is right on its own
merits: when a torrent stalls, searching for and dispatching a *different* release is precisely what
you want to do. So `downloading` keeps the whole acquisition path, and only a film on disk is
refused.

The row must also actually be on disk — `status = 'imported'` **and** a non-NULL `library_path`.
They agree in every row the importer writes, and requiring both means a half-written row refuses
nothing rather than refusing wrongly.

## Do

1. **One check in `handleDispatch`**, after the existing validation and before
   `dispatcher.Dispatch`, so a refusal costs nothing and leaves nothing behind. It already has
   `body.TMDBID`; `store.LibraryByTMDBID` is already on the `Store` interface for the badge on a
   poster, so there is **no new SQL and no new interface method** — and the badge and the refusal
   become the same query, which is why they cannot drift apart.
2. **409**, in the shape `failDelete` already uses for `ErrWrongCategory`: the request is
   well-formed, curator is working, and the refusal is deliberate. The body carries the sentence and
   names the path.
3. **One `Info` line.** A request that deliberately did nothing has to say so somewhere a human
   looks, and `/api/logs` is a screen.

## Do not

- **Refuse a film that is downloading.** See above. This is the stalled-torrent path.
- **Match on title and year when there is no `tmdb_id`.** `/search`'s release-name mode dispatches
  without one, so this cannot fire there, and that hole is deliberate: with no TMDB id there is no
  reliable identity for a film, and matching on title+year would be TMDB matching done in the wrong
  place — the same line `UpsertWantedMovie` already draws. Say so in the code rather than leaving it
  to be discovered.
- **Put the check in `internal/download`.** The library is not that package's question; it dispatches
  a release someone picked. `handleDispatch` already holds the store.
- **Rely on the UI for this.** T60 hides the buttons, but `details` is fetched once — a page open
  when the film finished importing in another tab still shows an enabled button. The server is the
  one that has to be right.

## Verify

Hermetic, `internal/api/downloads_test.go`:

- an `imported` `tmdb_id` → **409**, with the `{"error": …}` envelope, and the dispatcher was
  **never called**
- a `wanted` one → still 201
- **one mid-download → still 201**, which is the stalled-torrent case and the one an over-eager
  reading of the ask would break
- an `imported` row with a NULL `library_path` → not refused
- a request with no `tmdb_id` → unaffected
