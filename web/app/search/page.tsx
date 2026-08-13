'use client';

import { useState } from 'react';
import { api, type SearchResult } from '@/lib/api';
import { Failure, Working } from '@/components/states';
import { Releases } from '@/components/releases';

// Search and Releases are one screen, not two.
//
// A release id is only valid while the search that issued it is still cached —
// an hour (D10) — and resolving an expired one is a 410. A separate /releases/
// route would have to either search again, getting different ids, or stash the
// results somewhere and pretend. Rendering the list from the response already
// in hand is the only version that cannot go stale between the two halves.
export default function Search() {
  const [title, setTitle] = useState('');
  const [year, setYear] = useState('');
  const [quality, setQuality] = useState('');

  const [searching, setSearching] = useState(false);
  const [result, setResult] = useState<SearchResult | null>(null);
  const [error, setError] = useState<unknown>(null);

  async function runSearch(event?: React.FormEvent) {
    event?.preventDefault();
    if (!title.trim()) return;

    setSearching(true);
    setError(null);
    try {
      setResult(await api.search(title.trim(), year ? Number(year) : undefined, quality || undefined));
    } catch (e) {
      setError(e);
      setResult(null);
    } finally {
      setSearching(false);
    }
  }

  return (
    <>
      <h1>Search</h1>
      <p className="lede">
        Four indexers at once. A first search takes about thirteen seconds, because a real browser is
        clearing a Cloudflare challenge for 1337x; a repeat within the hour is instant.
      </p>

      <form className="row" onSubmit={runSearch}>
        <input
          name="title"
          placeholder="Interstellar"
          value={title}
          onChange={(e) => setTitle(e.target.value)}
          aria-label="Title"
          autoFocus
        />
        <input
          name="year"
          placeholder="Year"
          value={year}
          onChange={(e) => setYear(e.target.value.replace(/\D/g, '').slice(0, 4))}
          inputMode="numeric"
          size={6}
          aria-label="Year"
        />
        <select value={quality} onChange={(e) => setQuality(e.target.value)} aria-label="Quality">
          <option value="">Any quality</option>
          <option value="2160p">2160p</option>
          <option value="1080p">1080p</option>
          <option value="720p">720p</option>
        </select>
        <button className="primary" type="submit" disabled={searching || !title.trim()}>
          {searching ? 'Searching…' : 'Search'}
        </button>
      </form>

      {searching && (
        <Working
          what="Searching YTS, TPB and 1337x"
          hint="1337x needs a real browser to clear Cloudflare, so the first search for a title takes ~13s. The next one this hour is instant."
        />
      )}
      {error !== null && <Failure error={error} onRetry={() => runSearch()} />}

      {result && !result.year && (
        <div className="banner warn">
          <strong>Add a year before downloading</strong>
          <span className="small">
            The title and year you searched for become the library folder —{' '}
            <span className="mono">Title (Year)</span> — and curator will not guess a year it was not
            given. A release name is not a safe source for one either:{' '}
            <span className="mono">Blade Runner 2049 (2017)</span> would yield 2049. Search again
            with the year and the download button will work.
          </span>
        </div>
      )}

      <Releases
        result={result}
        // What was typed, echoed by the search — this screen has no better
        // source. The movie page does, which is why the film is a prop.
        film={{ title: result?.title ?? '', year: result?.year ?? 0 }}
        searching={searching}
        onSearchAgain={() => runSearch()}
        empty={
          result && (
            <>
              Nothing found for <strong>{result.title}</strong>
              {result.year ? ` (${result.year})` : ''}. Try dropping the year, or a shorter title.
            </>
          )
        }
      />
    </>
  );
}
