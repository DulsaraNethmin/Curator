# T34 — the backend-neutral torrent

**Owns:** `internal/torrent/` — `torrent.go`, `torrent_test.go`; the signatures in
`internal/download/{service,poller}.go`, `internal/importer/importer.go` and `internal/qbit/`
**Depends on:** nothing

## Goal

One `Torrent` type that says nothing about which backend produced it, so T35 can add a second one
without the two growing two shapes. **No behaviour change, its own commit** — the same discipline as
T31's `<Releases>` extraction, and for the same reason.

There are exactly **12** non-test references to `internal/qbit` outside that package today, spread
over five files. When this task is done there is **1** — `cmd/curator/main.go`, where a backend is
chosen. That is the acceptance test, and it is one `grep`.

## Do

1. **`internal/torrent`, a leaf package** — stdlib imports only, so every other package can depend
   on it and none of them pays for a dependency. It holds three things:
   - `Torrent` with the six fields that are actually read: `Hash`, `Name`, `State`, `Progress`,
     `ContentPath`, `Category`. `Size` and `SavePath` are decoded by `internal/qbit` today and read
     by nobody, so they do not cross over. A field nothing reads is a field that is wrong without
     anyone noticing.
   - the four states as untyped string constants, moved from `qbit/state.go`. They are the same four
     strings `store.Download*` carries, which is why they must be *moved* and not *added*: three
     parallel sets of the same constants is how they drift.
   - `NormalizeHash`, which upper-cases. See below.
2. **`Torrent.State` carries curator's four, not the backend's vocabulary.** This is the whole point
   of the type. `qbit.MapState`'s 23-entry map stays in `internal/qbit` and runs on the way out,
   where a backend's private vocabulary belongs; `download` and `importer` stop knowing that
   `stalledUP` is a word.
3. **`Hash` is upper-case**, the form `indexer.InfoHash` produces and the `downloads` table stores.
   qBittorrent reports lower-case and its `hashes=` filter demands it, so lower-case becomes an
   internal detail of one backend's wire protocol — which is what it always was. `internal/qbit`
   normalises on the way out; the poller's `qbit.NormalizeHash(t.Hash)` becomes `t.Hash`.
4. **`ErrWrongCategory` moves too.** `internal/api/movies_delete.go` tests for it with `errors.Is` to
   answer 409, and an API layer reaching into a backend for an error value is the coupling this task
   exists to remove. `internal/qbit` wraps `torrent.ErrWrongCategory`; `errors.Is` keeps working
   unchanged.
5. **The three interfaces take `torrent.Torrent`**: `download.TorrentClient` (all four methods),
   `download.Importer`, `download.ManualImporter` — and therefore `importer.Import` and
   `importer.TryImport`.
6. **`internal/qbit` keeps its wire struct, unexported.** The JSON shape qBittorrent sends is that
   package's business and stays documented there, including the two fields nothing reads. What
   crosses the package boundary is a `torrent.Torrent`.

## Do not

- Put the type in `internal/download`. `internal/qbit` would then have to import the orchestration
  layer to satisfy an interface, which is backwards and makes the client un-extractable.
- Move `download.TorrentClient` into `internal/torrent`. The consumer declares the interface it
  needs; that is where it already is and it is right.
- Change what any method does, what any error says, or the order of anything. The one accepted
  difference is named below.
- Touch `internal/api`'s responses, the schema, or the UI. Nothing observable over HTTP changes.
- Add a dependency. This task's package imports `strings` and nothing else.

## The one accepted behaviour difference

`download/service.go`'s "not completed" error reads

> qBittorrent reports "stalledDL", which is "queued"

and can no longer, because the raw state stops crossing the boundary. It becomes curator's mapped
state alone. That is the cost of the type, it is one error message on the manual-import path, and
adding a seventh field to carry a diagnostic string would be paying for it in the wrong currency.

## Verify

```bash
go build ./... && go vet ./... && go test -race ./... && GOOS=linux GOARCH=arm64 go build ./...

# the acceptance test: one importer of internal/qbit, and it is main.go
grep -rln '"github.com/DulsaraNethmin/curator/internal/qbit"' --include="*.go" .
```

- every existing test in `internal/download`, `internal/importer`, `internal/api` and `internal/qbit`
  still passes, with only the fakes' types changed — **if an assertion had to change, the task
  overreached**
- `internal/torrent` has a test that the four states are the four strings `store` carries, so the
  two sets cannot drift apart silently
- `NormalizeHash` upper-cases, trims, and leaves an already-upper hash alone
- `qbit.Client` still satisfies `download.TorrentClient`, which `cmd/curator` already asserts at
  compile time by assigning one to a variable of that interface type — no `var _` line is needed and
  none is added, because it would have to live in one of the two packages this task just separated
