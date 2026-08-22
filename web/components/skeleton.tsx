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

/** Rails the shape of <Rail> — heading bar, then a strip of card shapes. */
export function SkeletonRails({ rails = 3, cards = 7 }: { rails?: number; cards?: number }) {
  return (
    <>
      {Array.from({ length: rails }, (_, rail) => (
        <section className="rail-section" key={rail}>
          <div className="skel skel-rail-title" />
          <div className="rail">
            {Array.from({ length: cards }, (_, card) => (
              <SkeletonCard key={card} />
            ))}
          </div>
        </section>
      ))}
    </>
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
