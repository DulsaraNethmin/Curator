# T95 — the UI

**Owns** the screens of [phase 11](../phase-11.md) · **needs** T94's route shapes

## What it owns

`web/lib/api.ts` — *the only place in the UI that calls fetch* — gains the types and methods.
`/library/` gains a **Movies | Shows** toggle. `/show/?id=` mirrors `/movie/?id=`, because
[D21](../decisions.md#d21--the-movie-page-is-movieid-because-the-ui-is-a-static-export) means a
static export cannot have dynamic segments. `<Releases>` gains a season selector. Discover gains TV
rails.

**Every TV affordance is absent when `LIBRARY_TV` is unset**, not present and broken. That is what
`config.TVConfigured()` is for, and it is what makes television genuinely opt-in.

## Binding

The build is two commands and the order matters
([D16](../decisions.md#d16--the-ui-is-embedded-with-all-and-a-committed-placeholder-keeps-go-build-honest)):
`npm --prefix web run build` writes `internal/web/dist/`, and only then does the Go build embed it.
