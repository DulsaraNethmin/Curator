'use client';

import { useEffect, useState } from 'react';
import { Nav } from '@/components/nav';

/**
 * The topbar, and when it earns its ground.
 *
 * At rest at the top of a page nothing has scrolled under the bar, so a panel
 * fill there is a lid on content that is not moving — it sits transparent and
 * picks up the panel, the border and the blur the moment anything passes
 * beneath. One passive scroll listener; the styles are the two `.topbar`
 * states in globals.css.
 *
 * This is a client component so the LAYOUT does not have to be one: it owns
 * exactly the header, and the rest of the shell stays as it was.
 */
export function Topbar() {
  const [scrolled, setScrolled] = useState(false);

  useEffect(() => {
    // Read once on mount too: a reload halfway down a page starts scrolled.
    const read = () => setScrolled(window.scrollY > 8);
    read();
    window.addEventListener('scroll', read, { passive: true });
    return () => window.removeEventListener('scroll', read);
  }, []);

  return (
    <header className="topbar" data-scrolled={scrolled}>
      <div className="topbar-inner">
        <a className="brand" href="/">
          curator<span>one binary</span>
        </a>
        <Nav />
      </div>
    </header>
  );
}
