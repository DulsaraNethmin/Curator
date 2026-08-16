'use client';

import { useEffect, useState } from 'react';
import { api, ApiError, type MovieRow, type MovieSummary } from '@/lib/api';
import { MovieCard } from '@/components/movie-card';
import { Empty } from '@/components/states';

/**
 * MatchPicker points a library row at the right film — establishing a match on a
 * row with no `tmdb_id` (T67), or replacing one on a row that has the wrong one
 * (T69).
 *
 * **Which of the two it is comes from the row, not from a prop.** The component
 * already took the whole `MovieRow`, so `movie.tmdb_id !== null` answers it, and
 * the caller cannot open the picker in the wrong mode. It decides three things:
 * the sentence below the heading, the endpoint, and whether the film this row
 * already holds counts as a collision.
 *
 * **The search is seeded from the row, not typed from scratch.** The folder
 * already carries a title and a year — they are what the scan searched with and
 * failed on — so the first query runs on mount and the box opens with the answer
 * already on screen. Both stay editable, because a folder name TMDB could not
 * resolve is exactly the string most likely to need one word changed, and a year
 * parsed off a folder is exactly the field most likely to be the reason it
 * failed.
 *
 * `GET /api/tmdb/search` needs nothing new for this. Its doc comment already
 * anticipated it: pasting a library folder name into the search box is a real
 * thing to do while looking at the Library screen.
 *
 * This renders only where a key is in force. The caller checks that against
 * `integrations`, and the reason it matters is in the page above.
 */
export function MatchPicker({
  movie,
  onMatched,
  onCancel,
}: {
  movie: MovieRow;
  onMatched: (next: MovieRow) => void;
  onCancel: () => void;
}) {
  // Which of the two jobs this is, read off the row rather than passed in. The
  // page that opens the picker does not have to know, and cannot get it wrong.
  const correcting = movie.tmdb_id !== null;

  const [query, setQuery] = useState(movie.title);
  // A year of 0 is "no year", which is a real state for a wanted row. It shows
  // as an empty box rather than a literal 0 nobody typed.
  const [year, setYear] = useState(movie.year > 0 ? String(movie.year) : '');
  const [results, setResults] = useState<MovieSummary[] | null>(null);
  const [looking, setLooking] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  // The id being written, so one card can say so without disabling the rest
  // ambiguously. null means nothing is in flight.
  const [picking, setPicking] = useState<number | null>(null);

  // The seeded search, once, on the terms the folder already carries.
  useEffect(() => {
    void find(movie.title, movie.year > 0 ? String(movie.year) : '');
    // Seeding is a mount-time action and re-running it on every keystroke is
    // exactly what the editable box exists to avoid.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function find(q: string, y: string) {
    const trimmed = q.trim();
    if (trimmed === '') return;

    setLooking(true);
    setFailure(null);
    try {
      const answer = await api.tmdbSearch(trimmed, y === '' ? undefined : Number(y));
      setResults(answer.results);
    } catch (cause) {
      setFailure(cause instanceof Error ? cause.message : String(cause));
      setResults(null);
    } finally {
      setLooking(false);
    }
  }

  async function pick(film: MovieSummary) {
    if (picking !== null) return;

    // tmdb_id is UNIQUE, so this is the API's 409 said one step earlier and
    // without a round trip. The card already carries the badge that explains it;
    // this is what happens when somebody clicks it anyway.
    //
    // **`movie_id !== movie.id` is the correction case and it must not be
    // refused.** In correction mode the film this row already holds is in the
    // results and carries the badge — it is in the library, under THIS folder —
    // so a bare `film.library` check would refuse somebody re-picking the film
    // they are already matched to, and the sentence would name a collision with
    // their own row. The server draws the same line: CorrectMatch's taken probe
    // ignores the row being corrected.
    if (film.library && film.library.movie_id !== movie.id) {
      setFailure('curator already has that film in the library under another folder.');
      return;
    }

    setPicking(film.tmdb_id);
    setFailure(null);
    try {
      // POST establishes a match and PUT replaces one; which of those this is
      // depends on the row, not on a prop, so the caller cannot get it wrong by
      // opening the picker from the wrong place.
      onMatched(
        correcting
          ? await api.correctMatch(movie.id, film.tmdb_id)
          : await api.matchMovie(movie.id, film.tmdb_id),
      );
    } catch (cause) {
      // The API's own sentence, verbatim. Both 409s were written to be read —
      // "already matched" and "another folder already holds that film" are
      // different mistakes with different remedies, and a generic banner would
      // throw away the only part that says which.
      setFailure(cause instanceof ApiError ? cause.message : String(cause));
      setPicking(null);
    }
  }

  return (
    <section className="matcher">
      <h2>Which film is this?</h2>
      {/* Two situations, two sentences. The original one describes a search that
          found nothing, which is a plain falsehood on a row that IS matched —
          something did resolve, it was simply the wrong film. Saying so also
          says what will happen, because a correction overwrites rather than
          clearing: there is no moment where the row has no film. */}
      <p className="lede">
        {correcting ? (
          <>
            This folder is matched to a film. Pick the right one and it replaces the match — the
            poster, the overview and the Jellyfin link all follow it, and a later scan will not undo
            it.
          </>
        ) : (
          <>
            curator searched TMDB for <span className="mono">{movie.title}</span> and found nothing it
            was sure of. Pick the right film and it keeps the match — a later scan will not undo it.
          </>
        )}
      </p>

      <form
        className="matcher-search"
        onSubmit={(event) => {
          event.preventDefault();
          void find(query, year);
        }}
      >
        <input
          aria-label="Film title"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
          placeholder="Title"
        />
        <input
          aria-label="Year"
          value={year}
          onChange={(event) => setYear(event.target.value.replace(/\D/g, '').slice(0, 4))}
          placeholder="Year"
          inputMode="numeric"
          size={4}
        />
        <button type="submit" disabled={looking || query.trim() === ''}>
          {looking ? 'Searching…' : 'Search'}
        </button>
        <button type="button" onClick={onCancel} disabled={picking !== null}>
          Cancel
        </button>
      </form>

      {failure && (
        <p className="banner error" role="alert">
          {failure}
        </p>
      )}

      {picking !== null && <p className="lede">Matching…</p>}

      {results !== null &&
        (results.length === 0 ? (
          <Empty>
            TMDB has nothing for that. Try the title without the year, or the name the film was
            released under rather than the one on the folder.
          </Empty>
        ) : (
          <div className="grid">
            {results.map((film) => (
              // Every card is a button, including one curator already has: it
              // carries the badge that says so, and clicking it answers with the
              // reason rather than navigating away from the row being matched.
              <MovieCard key={film.tmdb_id} film={film} onPick={pick} />
            ))}
          </div>
        ))}
    </section>
  );
}
