# T58 — a `library_path` outside the root stops blocking its own deletion

**Owns:** `library.ErrOutsideRoot` in `internal/library`, one branch in
`download.Service.DeleteMovie`, `Deletion.FolderLeft`, the Library screen's deleted banner
**Depends on:** [D19](../decisions.md#d19--deleting-a-movie-removes-the-file-and-asks-qbittorrent-to-remove-its-own)
(the delete sequence), [T57](T57-library-way-in.md) (which is what makes such a row reachable)

## Goal

`DELETE /api/movies/{id}` works on the one row that most needs it.

Step 2 of `Service.DeleteMovie` calls `importer.RemoveFromLibrary` →
`library.RemoveMovieFolder`, which refuses a path outside `LIBRARY_MOVIES` and returns an error. The
error propagates, step 3 never runs, and **the request 500s with the row still there**. A row
pointing outside the root can never be served — `stream.go` refuses it with the same containment
check — so it is a row nothing can use and nothing can remove.

T57's prune clears these on a scan. This is the other half: the **button** has to work too, because
a user looking at a card is not going to reason about which endpoint reaches it.

## Do

1. **A sentinel, not a weakened check.** `library.ErrOutsideRoot`, wrapped by `RemoveMovieFolder` on
   **both** refusal paths — the root itself, and a path that escapes it — and by `AssertInside`.
   Named for the reason `ErrNoVideo` is: the caller has to be able to tell a **refusal** from a
   **failure**.

2. **`Service.DeleteMovie` treats that one error as "there is nothing of ours there."** Log a Warn
   naming both paths, then carry on to step 3 and remove the rows. Every other error still aborts,
   and the ordering D19 argues for is untouched.

3. **The report must not lie.** `Deletion` gains `FolderLeft string \`json:"folder_left,omitempty"\``
   — absent in the ordinary case, like `jellyfin_url` and `remux_url`. The Library screen's banner
   says "The library folder was removed", which would be false for exactly this delete, so it
   branches on the new field.

## Do not

- **Weaken `RemoveMovieFolder`'s containment check.** It is the whole safety argument: a
  `library_path` that had drifted — or been crafted — into `/` would otherwise be an `rm -rf` with a
  friendly name. The sentinel gives the caller the outcome it needs without touching the guard.
- **Put the decision in `internal/library`.** That package deletes a folder or refuses to; what a
  refusal *means* for a database row is the delete service's, which is where the sequence lives.
- **Silence it.** A row pointing outside the library root is an operator's problem — a repointed
  `LIBRARY_MOVIES`, a database restored beside a different library — and the log line naming both
  paths is where it gets diagnosed. Same posture `stream.go` already takes.
- Change what a delete does for a row **inside** the root. That path is unchanged, folder and all.

## Verify

Hermetic:

- `RemoveMovieFolder` wraps `ErrOutsideRoot` for a path outside the root **and** for the root itself,
  asserted with `errors.Is` rather than on the message.
- `AssertInside` wraps it too, so one sentinel covers both questions.
- `DeleteMovie` on a row whose folder is outside the root: **succeeds**, the rows are gone, the
  folder is **still on disk**, and `FolderLeft` names it.
- A row inside the root: folder removed, `FolderLeft` empty — the ordinary case is untouched.
- A folder that is already gone still succeeds and reports no `FolderLeft`, which is the "already
  gone is success" rule `RemoveMovieFolder` already keeps.
- `next build`'s type check covers the screen: `Deletion` gains the field in `web/lib/api.ts`.

Then live, after T57's scan: a card whose `library_path` points at the old `testdata/` root deletes
from the UI instead of turning the banner red.
