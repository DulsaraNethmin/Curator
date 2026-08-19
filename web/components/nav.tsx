'use client';

import { usePathname } from 'next/navigation';
import Link from 'next/link';

// trailingSlash: true is set in next.config.mjs, so every href here carries one
// and the exported files are directories holding index.html — the layout
// http.FileServer resolves without any rewrite rules on the Go side.
const screens = [
  // Discover is '/', so it is the one href without a trailing segment to slice.
  { href: '/', label: 'Discover' },
  { href: '/search/', label: 'Search' },
  { href: '/library/', label: 'Library' },
  { href: '/activity/', label: 'Activity' },
  { href: '/vpn/', label: 'VPN' },
  { href: '/logs/', label: 'Logs' },
  { href: '/settings/', label: 'Settings' },
];

export function Nav() {
  const pathname = usePathname();

  return (
    <nav>
      {screens.map((screen) => (
        <Link
          key={screen.href}
          href={screen.href}
          data-active={pathname === screen.href || pathname === screen.href.slice(0, -1)}
        >
          {screen.label}
        </Link>
      ))}
    </nav>
  );
}
