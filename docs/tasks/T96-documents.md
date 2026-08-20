# T96 — the documents

**Owns** the prose of [phase 11](../phase-11.md) · one lane and one mind, deliberately

## Why it is one lane

CLAUDE.md measured this on T51: `README.md` and `docs/architecture.md` describe **the same pipeline
in two notations** — an ASCII fence and a mermaid `sequenceDiagram` — so splitting them produces two
different stories about where the VPN sits. They have drifted before. The realistic ceiling on a
documentation task here is ~2x, and this one is under it.

## What it owns

- `roadmap.md` — the phase table, and the bullet reading *"**TV.** Retired by choice in D43, not
  deferred"*, which [D48](../decisions.md#d48--television-is-additive-a-show-is-a-row-in-movies-and-the-second-library-root-is-opt-in)
  reopens.
- `README.md` — the ASCII pipeline fence, and *"**No television.** Movies only."*
- `architecture.md` — the `sequenceDiagram`, the `Indexer` code fence, the `erDiagram`, the
  components table, **and** the EZTV row that has said *"TV, a later phase"* since phase 2.
- `compose.yaml` / `compose.pi.yaml` — `LIBRARY_TV`. The Pi's `media` bind already carries `tv/` on
  the same filesystem as `downloads`, so [D8](../decisions.md#d8--import-by-hardlink)'s hardlink works.
- `docs/progress.md`, and CLAUDE.md's environment table, which does not currently mention `tv/`.

## What it must not do

**Do not edit [D26](../decisions.md#d26--television-keeps-its-stack-the-cutover-removes-only-what-curator-replaces-for-movies)
or [D43](../decisions.md#d43--the-pi-is-a-clean-slate-television-is-retired-and-curator-is-the-only-downloader).**
`T51-documents.md` is explicit: decisions are amended by later decisions and cross-linked, never
rewritten. D43 retired a *stack*, not a *capability*, and says so itself — quote it rather than
correcting it.

Mermaid must render before committing; `click` is a reserved word in flowcharts and a bad node name
fails silently on GitHub.
