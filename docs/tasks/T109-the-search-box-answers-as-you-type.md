# T109 — the search box answers as you type

**Owns** `web/lib/debounce.ts`, `/search/`'s one search effect, and the manual matcher's ·
**strengthens** the rule already written at the head of `web/app/search/page.tsx` — *"a release
search launches a browser for up to thirteen seconds, and no URL should do that on sight"* ·
**does not overturn** [D5](../decisions.md#d5--manual-search-not-automatic-grabbing): search is still
something a person does, and nothing here grabs anything · **decides nothing** — see the foot.

## What it owns

**The film search runs as you type. The release search still does not.** That asymmetry is the whole
design and it is the thing most at risk from a tidy-up, so it is measured below rather than asserted:
a TMDB search is one call in ~150 ms; a release search launches a real browser through minter and
re-hits YTS, TPB and EZTV live, and the server cache wraps only 1337x.

**Three ways in became one effect.** A shared `?q=` link, typing, and switching media were three code
paths asking the same question, which is three places for the media type to be wrong. The old `auto`
ref becomes `searched` and widens from *"the URL search has run"* to *"this is what has been asked"*,
which is what stops any of the three repeating another's work. `chooseMedia` no longer runs a search
itself — `media` is in the effect's key and dependency list, so the switch re-searches on its own,
and doing it in both places was one double-fire away from two requests per click.

**`useDebounced` returns its first value immediately.** That is what keeps a shared link searching on
arrival rather than 300 ms after it, and it is also what let the manual matcher drop an
`eslint-disable-next-line react-hooks/exhaustive-deps`: its seeded search was a mount-only effect
precisely because re-running per keystroke was what the editable boxes existed to avoid.

**300 ms**, argued off a number this UI has already judged imperceptible — `web/components/preview.tsx`
opens a hover preview at 350 ms.

## The guard without which this would be unusable

`request` in `web/lib/api.ts` grew three lines:

```ts
if (init?.signal?.aborted) throw cause;
```

An aborted fetch rejects into the **same catch as a dead server**, so before this every superseded
keystroke would have painted *"cannot reach curator at this origin"* over a screen that is working
perfectly. It is rethrown as-is rather than wrapped, so `err.name === 'AbortError'` still answers at
the call site — the same check `discoverStream` already makes with `signal.aborted` (T104).

Cancellation itself is not optional: neither fetcher was ordered before, so a slow first answer
overwrote a fast second one, and debouncing makes that likelier rather than rarer.

## MEASURED — do not re-derive

Headless Chrome 151 against a throwaway curator at `:8097`, typing with real `Input.dispatchKeyEvent`
so React's `onChange` fires exactly as it would for a person. Counted at the wire.

```
a ?q=interstellar link, on arrival     1 request · 20 cards        (and not 300ms late)

"severance"      9 keys @180ms/key     1 request · 12 cards · ?q=severance
"interstellar"  12 keys @180ms/key     1 request · 20 cards · ?q=interstellar
"severance"      9 keys  @60ms/key     1 request · 12 cards · ?q=severance

"cannot reach curator" banner          none                        <- the abort guard
```

**The rule that must not change**, and the one to re-run if anybody touches this screen:

```
releases mode, 12 keystrokes           /api/search        0 requests
                                       /api/tmdb/search   0 requests
```

**Superseding an in-flight request**, which fast typing never produces — so this pauses mid-word past
the debounce on purpose, starting a request the rest of the word then overtakes:

```
type "inter", pause 900ms              1 request  (the prefix search goes)
type "stellar"                         2 requests total
box reads                              "interstellar"
url                                    ?q=interstellar     <- the box, not the prefix
cards                                  20
error banner                           none
```

The URL and the grid agreeing with the box rather than with the prefix is the ordering guarantee: the
first answer did not land on top of the second.

**The media switch**, which used to fire twice for one click:

```
press Shows                            1 request
                                       /api/tmdb/search?query=interstellar&media=tv
```

## TRAPS found the hard way

- **An abort looks exactly like an unreachable server** to `request`'s catch. Anybody adding a second
  cancellable call must keep the `signal.aborted` check ahead of the `ApiError(0, …)` — removing it
  is invisible until somebody types quickly.
- **A debounced *callback* would fire with a stale closure.** `findFilms` is a new function every
  render, so a debounced callback needs a ref to stay stable, and that ref is how it ends up
  searching the previous media type. Debouncing the *value* and letting the effect depend on it keeps
  the dependency list honest — which is why this is a value hook.
- **`finally { setLooking(false) }` must not run on an abort.** The newer search is still in flight,
  so clearing the spinner there makes the screen claim it is idle while a request is outstanding.

## Still outstanding

- **No automated test, and this repo adds no framework to get one.** The hook needs React and the
  abort needs `fetch`; what pins the behaviour is `next build`'s typecheck plus the counted run
  above, and what protects the rule is the comment at the head of `search/page.tsx`. This is the
  position T99 took in as many words.
- **`SEARCH_DEBOUNCE_MS` is not in `make units`.** It is a constant, and the hook around it cannot
  run without React — `check-lib.mjs` deliberately holds only modules that are pure.
- **The matcher's debounce is not measured**, only built and typechecked. It is the same hook on the
  same call, but the run above never opened it.

## Why no decision record

The rule this rests on — *the film search may auto-fire and the release search may never* — already
exists as a comment at the head of `web/app/search/page.tsx` and is strengthened here rather than
created. The one genuinely new invariant, *an aborted request is not a transport failure*, is three
lines and a comment inside `request`; a decision record for it would be a second place to keep in
step with the code it describes. If it is ever promoted, it belongs beside
[D54](../decisions.md#d54--discover-streams-and-the-browser-reads-it-with-fetch-rather-than-eventsource),
where the fetch-versus-EventSource reasoning already lives.

## Verification

```
DevTools → Network, filter /api/tmdb/search
type "severance" at a human rate      → 1 request
/search/?q=interstellar, cold          → 1 request, fired on arrival
press Shows with text in the box       → 1 request, media=tv
switch to release names, type 12 chars → 0 requests          ← the one that matters
```
