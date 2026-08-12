# T26 — Library, Activity and Settings

**Owns:** `web/app/library/`, `web/app/activity/`, `web/app/settings/`
**Depends on:** T24, T23

## Goal

The other half: what curator already has, what it is doing right now, and whether it is set up.

## Do

### Library — `GET /api/movies`, `POST /api/scan`

1. The 29 folders, with title, year, quality, size and status.
2. **Render the real data, which is full of nulls.** `tmdb_id`, `overview` and `poster_path` are
   nullable by design ([D6](../decisions.md#d6--tmdb_id-is-nullable)) and `library_path` is null for a
   wanted film. **15 of the 29 folders on the Pi are empty**, so `size_bytes` is 0 for over half the
   library on the very first screen anyone opens. A poster placeholder is not a nicety here; it is
   the majority case.
3. Surface **unmatched** films rather than hiding them. A `tmdb_id` of null is the row that wants
   human attention — that is the entire reason D6 made the column nullable instead of dropping the
   row.
4. A scan button posting to `POST /api/scan`, reporting the `{scanned, added, matched, unmatched}` it
   answers with. A cold scan takes ~9 s and a rescan 0.016 s, so the button needs the same
   honest-progress treatment as search.

### Activity — `GET /api/downloads`, `POST /api/downloads/{hash}/import`

5. Every download with its state and progress, **re-fetched on a timer**. No websocket and no SSE:
   the data is a `DOWNLOAD_POLL_INTERVAL` poll at source, so a second transport would add a moving
   part and no freshness. Stop the timer when the tab is hidden.
6. The five states are `queued`, `downloading`, `completed`, `imported`, `failed`. `completed` means
   the file is on disk but not yet in the library, and `imported` is where a download stops.
7. Show the manual import button **only** for a row that is `completed` — that is precisely the state
   [phase 4](../phase-4.md) built the endpoint for. Map its statuses to real words: `409` not
   finished, `422` nothing importable in the content path, `502` qBittorrent unreachable, `503`
   unconfigured.
8. A `failed` row is reported and **nothing is offered to retry it**
   ([D5](../decisions.md#d5--manual-search-not-automatic-grabbing)) — a human picks another release.

### Settings — `GET /api/settings`

9. Each integration as configured / not configured, with the environment variable to set when it is
   not. **qBittorrent is unconfigured right now**, so that is the state to design for first rather
   than an afterthought.
10. Reachability behind an explicit "check" action, because probing calls out to real services and
    one of them may wake a browser. The page loads instantly from configuration alone.
11. Paths and intervals as reported. An **empty** `DOWNLOADS_PATH` means "use qBittorrent's path
    verbatim" and must read as a deliberate setting, not as a missing one.

## Do not

- Show a secret, ask for one, or offer to edit anything
  ([D17](../decisions.md#d17--settings-is-read-only-and-the-settings-table-stays-unused)). There is no
  authentication in front of this page.
- Offer delete, remove-torrent, pause or resume. `internal/qbit` cannot do any of them on purpose,
  and the *arr stack shares that qBittorrent until phase 6.
- Offer to retry a failed download, or to re-import an already `imported` row.
- Assume a poster, an overview, a `tmdb_id`, a `library_path` or a non-zero size exists.
- Poll faster than the server reconciles. Below `DOWNLOAD_POLL_INTERVAL` it is load with no new data.

## Verify

In a browser, against a running curator with the fixture library:

- 29 movies render; the 15 zero-size, posterless ones look deliberate rather than broken
- unmatched films are visibly distinguishable from matched ones
- a scan reports its counts, and a rescan reports `added: 0`
- Activity's rows advance without a reload, and the timer stops when the tab is hidden
- the import button appears only on `completed` rows, and its error statuses read as sentences
- Settings reports qBittorrent **not configured** and names `QBIT_USER`
- the reachability check runs only when asked, and a dead Jellyfin renders as unreachable rather than
  as a broken page
- an empty `DOWNLOADS_PATH` reads as intentional
