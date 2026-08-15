'use client';

import { useRef, useState } from 'react';

/**
 * A command with a copy button that works without TLS.
 *
 * `navigator.clipboard` is undefined outside a secure context, and this product
 * is served over plain HTTP on a LAN address by design (D25) — so the copy
 * button is unavailable on exactly the install this was written for. It falls
 * back to selecting the text, which is one keystroke from the same result, and
 * says so rather than failing silently.
 *
 * Its own module because phase 9 has two pasted commands, not one: Jellyfin's
 * profile and minter's. The fallback above is the reason that matters — two
 * copies of it is two chances for the second screen to be the one that fails
 * silently on the install both were built for.
 */
export function Copyable({ text }: { text: string }) {
  const [state, setState] = useState<'idle' | 'copied' | 'select'>('idle');
  const box = useRef<HTMLElement>(null);

  async function copy() {
    try {
      await navigator.clipboard.writeText(text);
      setState('copied');
    } catch {
      // No clipboard, so select it instead and let the keyboard finish.
      const node = box.current;
      if (node) {
        const range = document.createRange();
        range.selectNodeContents(node);
        const selection = window.getSelection();
        selection?.removeAllRanges();
        selection?.addRange(range);
      }
      setState('select');
    }
  }

  return (
    <div className="row" style={{ alignItems: 'center', gap: '.75rem', flexWrap: 'wrap' }}>
      <code className="mono command" ref={box}>
        {text}
      </code>
      <button type="button" onClick={() => void copy()}>
        Copy
      </button>
      {state === 'copied' && <span className="small muted">copied</span>}
      {state === 'select' && (
        <span className="small muted">
          selected — press ⌘C or Ctrl+C. (Copying needs HTTPS, and this page is not served over it.)
        </span>
      )}
    </div>
  );
}
