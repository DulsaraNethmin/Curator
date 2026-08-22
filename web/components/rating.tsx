/**
 * The score, drawn rather than typed.
 *
 * TMDB's vote_average is 0–10 and used to render as the glyph "★ 8.0", which
 * says a number and nothing else. The ring says how full 8.0 is at a glance and
 * keeps the number beside it for anyone who wants the figure — the arc is never
 * the only carrier of the value, which is what lets the colour bands be
 * supplementary rather than a WCAG problem.
 *
 * The bands are the palette's own three voices: --accent from 7 up, --warn from
 * 5, --error below. All three owe 3:1 as graphics (WCAG 1.4.11) against the
 * page and the panel, and are in check-contrast.py's PAIRS. The billboard
 * overrides them with the dark theme's literals in CSS, because that surface is
 * dark in both themes and the light theme's greens are invisible on it.
 *
 * Zero renders nothing: TMDB uses 0 for "not enough votes", and an empty ring
 * beside "no date" would read as a rating of nought.
 */
export function Rating({ score, size = 'sm' }: { score: number; size?: 'sm' | 'md' }) {
  if (score <= 0) return null;

  const radius = 8;
  const circumference = 2 * Math.PI * radius;
  const arc = (Math.min(score, 10) / 10) * circumference;
  const band = score >= 7 ? 'high' : score >= 5 ? 'mid' : 'low';

  return (
    <span
      className={`rating rating-${size}`}
      data-band={band}
      role="img"
      aria-label={`Rated ${score.toFixed(1)} out of 10`}
    >
      <svg viewBox="0 0 20 20" aria-hidden="true">
        <circle className="rating-track" cx="10" cy="10" r={radius} />
        <circle
          className="rating-arc"
          cx="10"
          cy="10"
          r={radius}
          strokeDasharray={`${arc.toFixed(2)} ${circumference.toFixed(2)}`}
        />
      </svg>
      <span aria-hidden="true">{score.toFixed(1)}</span>
    </span>
  );
}
