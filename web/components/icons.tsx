/**
 * The whole icon set, drawn here rather than installed.
 *
 * Seven outline paths on a 24×24 grid, one stroke width, sized by the --icon-*
 * tokens in globals.css. The geometry follows Lucide's, which is fine to copy;
 * the npm package is not — an icon library is a runtime dependency and a build
 * step that has to still install in two years, for seven shapes that fit on one
 * screen. If an eighth icon is ever needed it goes in this map, at this stroke
 * width, and nowhere else.
 *
 * Icons are decoration by default: `aria-hidden` unless a `label` is given,
 * because every one of them currently sits beside the words it decorates and a
 * screen reader gains nothing from hearing "star" before "8.0". A label turns
 * it into the content instead — role="img" and the label as the name.
 */

export type IconName =
  | 'star'
  | 'chevron-left'
  | 'chevron-right'
  | 'arrow-right'
  | 'play'
  | 'pause';

const paths: Record<IconName, React.ReactNode> = {
  star: (
    <polygon points="12 2 15.09 8.26 22 9.27 17 14.14 18.18 21.02 12 17.77 5.82 21.02 7 14.14 2 9.27 8.91 8.26 12 2" />
  ),
  'chevron-left': <path d="m15 18-6-6 6-6" />,
  'chevron-right': <path d="m9 18 6-6-6-6" />,
  'arrow-right': (
    <>
      <path d="M5 12h14" />
      <path d="m12 5 7 7-7 7" />
    </>
  ),
  play: <polygon points="6 3 20 12 6 21 6 3" />,
  pause: (
    <>
      <rect x="14" y="4" width="4" height="16" rx="1" />
      <rect x="6" y="4" width="4" height="16" rx="1" />
    </>
  ),
};

export function Icon({
  name,
  size = 'md',
  label,
}: {
  name: IconName;
  size?: 'sm' | 'md' | 'lg';
  label?: string;
}) {
  return (
    <svg
      className={`icon icon-${size}`}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden={label ? undefined : true}
      role={label ? 'img' : undefined}
      aria-label={label}
    >
      {paths[name]}
    </svg>
  );
}
