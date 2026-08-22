/**
 * Placeholders sized to the content they become.
 *
 * "Loading…" as a paragraph answers the wrong question: it says the page is
 * busy, when what the eye wants is where things will be. A placeholder the
 * exact shape of the billboard, the rail or the grid lets the layout land once
 * — the poster fades in over the box that was always there — instead of the
 * whole page shifting when the data arrives, which is also what makes this the
 * CLS fix and not just a nicer spinner.
 *
 * The shimmer is the one infinite animation in the UI, allowed because it is a
 * loading indicator and nothing else may be. Under prefers-reduced-motion the
 * global rule stops it and a still block remains.
 *
 * Every visual here is aria-hidden; `Loading` is the one wrapper that speaks,
 * as a single polite status per screen. Two skeletons on one screen means one
 * of them is bare on purpose — a second "Loading" announcement says nothing
 * the first did not.
 */

export function Loading({ children }: { children: React.ReactNode }) {
  return (
    <div role="status" aria-live="polite">
      <span className="visually-hidden">Loading…</span>
      <div aria-hidden="true">{children}</div>
    </div>
  );
}

/** The billboard's shape — full bleed, same clamp — before trending answers. */
export function SkeletonBillboard() {
  return (
    <div className="skel skel-billboard" aria-hidden="true">
      <div className="skel-billboard-inner">
        <div className="skel-bar skel-eyebrow" />
        <div className="skel-bar skel-headline" />
        <div className="skel-bar skel-meta" />
        <div className="skel-bar skel-text" />
        <div className="skel-bar skel-text" />
      </div>
    </div>
  );
}

function SkeletonCard() {
  return (
    <div className="skel-card">
      <div className="skel skel-poster" />
      <div className="skel-bar skel-title" />
      <div className="skel-bar skel-meta" />
    </div>
  );
}

/**
 * One rail's strip of card shapes, under a heading the caller already drew.
 *
 * This is what a rail the stream has NAMED but not yet filled shows: the real
 * title is known from the opening event, so only the cards are a placeholder.
 * Same `.rail` class as the strip it becomes, so the geometry is identical and
 * the posters fade in over boxes that were already the right size.
 */
export function SkeletonRailStrip({ cards = 7 }: { cards?: number }) {
  return (
    <div className="rail" aria-hidden="true">
      {Array.from({ length: cards }, (_, card) => (
        <SkeletonCard key={card} />
      ))}
    </div>
  );
}

/** Rails the shape of <Rail> — heading bar, then a strip of card shapes. */
export function SkeletonRails({ rails = 3, cards = 7 }: { rails?: number; cards?: number }) {
  return (
    <>
      {Array.from({ length: rails }, (_, rail) => (
        <section className="rail-section" key={rail}>
          <div className="skel skel-rail-title" />
          <SkeletonRailStrip cards={cards} />
        </section>
      ))}
    </>
  );
}

/**
 * The one thing that speaks while the rails fill in.
 *
 * A streamed screen cannot use `Loading`: its skeletons are inside twelve real
 * sections now, and twelve polite live regions announcing "Loading…" is twelve
 * announcements of one fact. So the sections stay silent and this says it once.
 *
 * It is mounted whether or not anything is pending, and only its TEXT changes. A
 * live region added to the document with its message already in it may not be
 * announced at all — the region has to exist first for the change to be a
 * change — and the count is deliberately left out, because a number ticking from
 * twelve to zero is eleven more announcements nobody asked for.
 */
export function LoadingRails({ pending }: { pending: boolean }) {
  return (
    <div role="status" aria-live="polite" className="visually-hidden">
      {pending ? 'Loading…' : ''}
    </div>
  );
}

/** The poster grid's shape, for Library and the TMDB search. */
export function SkeletonGrid({ cards = 12 }: { cards?: number }) {
  return (
    <div className="grid">
      {Array.from({ length: cards }, (_, card) => (
        <SkeletonCard key={card} />
      ))}
    </div>
  );
}

/** The film screen's hero — poster beside text — before TMDB answers. */
export function SkeletonHero() {
  return (
    <div className="skel-hero" aria-hidden="true">
      <div className="skel skel-hero-poster" />
      <div className="skel-hero-text">
        <div className="skel-bar skel-headline" />
        <div className="skel-bar skel-meta" />
        <div className="skel-bar skel-text" />
        <div className="skel-bar skel-text" />
        <div className="skel-bar skel-text-short" />
      </div>
    </div>
  );
}

/** Prose-shaped lines, for the screens that are mostly sentences. */
export function SkeletonLines({ lines = 3 }: { lines?: number }) {
  return (
    <div className="skel-lines" aria-hidden="true">
      {Array.from({ length: lines }, (_, line) => (
        <div className="skel-bar skel-text" key={line} />
      ))}
    </div>
  );
}
