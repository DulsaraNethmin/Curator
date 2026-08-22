# T104 — Discover streams, and the screen lays out before TMDB answers

**Owns** `GET /api/tmdb/discover/stream`, the fan-out both discover routes now share, and the home
screen's arrival · **takes** [T102](T102-more-rails-and-a-visual-refresh.md)'s twelve rails and
per-rail cache unchanged and [T103](T103-motion-and-a-modern-discover.md)'s skeletons as the thing it
draws · **closes** the last item T102 and T103 both left open — *"Discover is one response, so a cold
cache waits for the slowest rail"* · **decides** [D54](../decisions.md#d54--discover-streams-and-the-browser-reads-it-with-fetch-rather-than-eventsource).

## What it owns

**The server.** `handleDiscover` grew a sibling. Everything the two would have to agree about came
out first — `beginDiscover` (the media and TMDB gates, and the library read), `eachRail` (the
concurrent fan-out, calling back once per rail as it resolves), `railRow` (one answer into one row) —
and the buffered handler is six lines over them. The stream opens with a `rails` event naming every
rail in draw order, sends one `row` event per rail, and ends with `done`.

**The client.** `web/app/page.tsx` reads it through `api.discoverStream`, holding
`DiscoverSlot[]` — `{id, title, row | null}` — instead of `DiscoverRow[] | null`. `Rail` takes a slot
and draws its real heading over `SkeletonRailStrip` until its row lands. The billboard now waits on
the trending rail alone.

## MEASURED — do not re-derive

**Against live TMDB, 12 rails, 240 cards, on a laptop.** Cold means a just-restarted binary; the rail
cache is in-memory and fifteen minutes (T102), so a restart is the only way to empty it.

```
                          COLD                          WARM
the one response          nothing at all for 754ms      1.2-1.8ms
                          and 817ms (two runs)
the stream, at the wire   rails    3.0ms                2.7-7.1ms, all twelve
                          row 1    811ms                in one flush
                          row 12   968ms
                          done     968ms
television (?media=tv)    rails 7.2ms, row 1 235ms, row 12 441ms, trending 4th at 345ms
```

**In headless Chrome, 1440x900, both themes:**

```
                                  COLD              WARM
twelve real rail headings         18-37ms           15-17ms
first real card                   736-810ms         15-17ms
the billboard                     860-899ms         15-17ms
all twelve rails filled           895-971ms         30ms
CLS                               0.00025 (one shift, ~40ms) — unchanged from T103
hOverflow                         0
```

**Payload:** 112,395 bytes buffered against 113,119 streamed. The SSE framing costs **724 bytes,
0.6%**, for 12 records.

### Read that table before repeating what this task was for

T102 and T103 both recorded the outstanding work as *"a cold cache waits for the slowest rail"*, and
the implied fix was that rails arriving separately would each arrive sooner. **That is not where the
time was.** The twelve rails are twelve parallel requests to one host, and they answer within about
**160ms of each other** — so sending them separately is worth ~150ms of the ~800.

What is worth the other 800 is the **opening event**. The page used to know nothing about its own
shape until the response landed, so it drew three generic skeleton rails and then became twelve. Now
it has twelve real headings at 18–37ms and only the cards are a placeholder. The win went to the
whole screen rather than to one rail.

The billboard did gain what it was supposed to gain, and it is smaller than the framing suggested:
**about 80ms on films** (899ms → 860ms was the measured pair; trending is simply not reliably last),
and about **100ms on television**, where the spread is wider — trending answered 4th at 345ms against
a last rail at 441ms.

**Where per-rail sending genuinely earns its keep is the tail**, and it is not visible in any of the
numbers above because it needs a rail to be slow. One wedged rail costs TMDB's ten-second timeout,
and on the buffered route that is ten seconds of blank home page for all twelve.
`TestARailIsSentWhileASlowerOneIsStillInFlight` is the whole of that claim, held over a real
connection.

## TRAPS

- **`httptest.ResponseRecorder` buffers.** A streaming test written against one asserts that bytes
  were written, never that they arrived early — which is exactly the failure `x-accel-buffering: no`
  is about. The ordering test uses `httptest.NewServer` and reads the socket. It contains no sleep:
  it blocks for eleven rows, and the twelfth cannot exist until the test releases it.
- **`x-accel-buffering: no` is not decoration.** nginx, Traefik and Caddy buffer a proxied body by
  default, which would hold every rail until the last one and turn this route silently back into the
  one it replaces. curator is a `docker run` somebody is likely to put a proxy in front of.
- **Comparing two `discoverRow`s with `%+v` compares pointer addresses.** `Library` is a
  `*libraryStateBody`, so the naive agreement test passes for every card with no badge and fails for
  every card that has one. Compare as JSON, which is the only thing either route promises.
- **A record is not a chunk.** 112 KB does not arrive in twelve tidy pieces, so the reader splits on
  the blank line and keeps the tail — everything after the last `\n\n` is a half-arrived record.
- **`Page.addScriptToEvaluateOnNewDocument` runs before `document.documentElement` exists.** A
  `MutationObserver` installed there throws on `observe()` and the timeline comes back empty, which
  reads as "nothing happened" rather than as a broken probe. `requestAnimationFrame` works.
- **The library read moved before the fan-out**, and it had to: a stream has written bytes by the
  time the last rail lands, so a store failure discovered there could not be a status code any more.
  Every refusal this screen has — 400, 503, 500 — now happens before the first write, and
  `TestTheStreamRefusesBeforeItOpens` pins that.
- **A live region inserted with its message already in it may not be announced.** `LoadingRails` is
  mounted whether or not anything is pending and only its text changes.

## What it deliberately did not do

- **No `EventSource`.** The reasoning is [D54](../decisions.md#d54--discover-streams-and-the-browser-reads-it-with-fetch-rather-than-eventsource);
  the short version is that it cannot see an HTTP status, so a 401 would never reach the login gate.
- **No reconnect, no `Last-Event-ID`, no `retry:`.** The stream lives for under a second and the
  screen already handles a failure by marking the unfilled rails failed. A resume protocol for a
  900ms request would be more state than the thing it protects.
- **No heartbeat comments.** Nothing here idles: the longest gap between events is one TMDB timeout,
  which is shorter than any proxy's idle limit.
- **The rails are still all requested at once.** Nothing was made lazy, and a rail below the fold is
  fetched with the rest. Doing otherwise would trade twelve parallel requests for twelve serial ones
  spread over a scroll, and the cache already makes the second visit free.
- **`api.discover` is gone from the client but `GET /api/tmdb/discover` is not.** The route is the
  plain `curl … | jq` shape and what the stream is checked against; a second path through the UI
  would be an untested one.
- **Nothing about the 15-minute clock changed.** It was the open design question and the answer was
  that there was nothing to answer: the cache was already per-rail and keyed `media/railID`, so a
  warm rail simply resolves immediately and streams in the first flush. `TestAStreamedRailFillsTheCacheTheOtherRouteReads`
  holds the two routes to one cache.
