// Package importer turns a completed download into something the library can
// see: a film in LIBRARY_MOVIES, or the episodes of a show in LIBRARY_TV.
//
// It is the one place that knows a torrent on disk and a library folder are the
// same title. It knows nothing about deployment paths: a backend that reports
// paths in a filesystem of its own translates them before they cross the
// interface, and internal/library is a pure package that must not learn about
// mounts.
//
// It is driven by the poller's existing torrent list rather than by a query of
// its own, and it triggers on a state — "this torrent reads completed and its
// row does not read imported" — rather than on the transition into completed,
// which is not crash safe. See docs/decisions.md D14.
//
// **The media type comes from the ROW, never from the files.** A download's
// content path can be read two ways — the largest video in it is a film, the
// numbered ones are episodes — and guessing between them from what is on disk
// is how a season pack becomes one enormous "movie". The row was written when
// the release was dispatched, by somebody who picked a show or a film on
// purpose (docs/decisions.md D48).
package importer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/DulsaraNethmin/curator/internal/library"
	"github.com/DulsaraNethmin/curator/internal/store"
	"github.com/DulsaraNethmin/curator/internal/torrent"
)

// destDirMode matches what qBittorrent and the *arr stack already create on the
// Pi: directories 0755, files 0644 (measured 2026-08-12). The linked file needs
// no mode of its own — a hardlink is a second name for one inode and carries the
// source's bits — so this is only about the folder being traversable by
// Jellyfin.
const destDirMode = 0o755

// Store is the persistence an import needs. Declared here rather than taken as
// *store.Store so this package is tested against a fake with no database, the
// pattern internal/api and internal/download already use.
type Store interface {
	GetMovie(ctx context.Context, id int64) (store.Movie, error)
	MarkImported(ctx context.Context, hash, libraryPath string, sizeBytes int64, at time.Time) (store.Movie, error)
}

// LibraryRefresher is the media server, if there is one. A nil refresher is a
// supported state and means no API key was configured — the same posture as a
// nil api.Matcher and a nil download.TorrentClient (docs/decisions.md D15).
type LibraryRefresher interface {
	RefreshLibrary(ctx context.Context) error
}

// Roots is where each media type is filed, and it is a STRUCT rather than two
// string parameters on purpose.
//
// The two are interchangeable to the compiler and catastrophic to swap: films
// would be written into LIBRARY_TV and seasons into LIBRARY_MOVIES, and the
// next scan would then delete every row it found in the wrong place for sitting
// outside its own root (docs/decisions.md D48). One named field per root makes
// that a thing you have to type on purpose. It is the same argument
// store.WantedMovie makes for taking a struct instead of four positional
// arguments.
type Roots struct {
	// Movies is LIBRARY_MOVIES. It always has a value: config defaults it.
	Movies string

	// TV is LIBRARY_TV, and **empty means television is off** — no Shows tab, no
	// TV rails, and no root to file an episode into. Empty is a supported state
	// and not a broken one, so it is refused where it is USED rather than at
	// construction: an install with no television configured must build an
	// importer, import films, and never notice this field exists.
	TV string
}

// For picks the root a media type is filed under.
//
// The default case REFUSES rather than falling back to films, and that is the
// whole reason this is a function. `media_type` is `NOT NULL DEFAULT 'movie'`
// and validated on every write since T88, so a row can only hold "movie" or
// "tv" — which means an unrecognised value is a bug or a hand-edited database,
// and the correct answer to "I do not know what this is" is never "put it with
// the films". A wrong guess here writes a stranger's files into a library root
// and gets the row deleted by the next scan of the other one.
//
// Both refusals wrap library.ErrOutsideRoot, which is what makes them safe for
// RemoveFromLibrary's caller: download.Service reads that sentinel as "there is
// nothing of ours in there to remove", takes the rows and leaves the disk alone
// (docs/decisions.md D19). With no LIBRARY_TV that is exactly true — curator
// has no television root, so no folder on disk is its to delete — and it keeps
// such a row deletable instead of permanently stuck.
func (r Roots) For(mediaType string) (string, error) {
	switch mediaType {
	case store.MediaTypeMovie:
		return r.Movies, nil
	case store.MediaTypeTV:
		if strings.TrimSpace(r.TV) == "" {
			return "", fmt.Errorf(
				"this is a show and LIBRARY_TV is unset, so there is nowhere to file it: %w",
				library.ErrOutsideRoot)
		}
		return r.TV, nil
	default:
		return "", fmt.Errorf("%q is not a media type (%q or %q): %w",
			mediaType, store.MediaTypeMovie, store.MediaTypeTV, library.ErrOutsideRoot)
	}
}

// Importer hardlinks completed downloads into the library.
type Importer struct {
	store     Store
	roots     Roots
	refresher LibraryRefresher
	log       *slog.Logger
	now       func() time.Time

	mu sync.Mutex
	// warned suppresses a repeat log for a failure that has not changed. D14
	// retries a failing import on every tick by design, and without this a
	// permanently broken one would say so every ten seconds for ever. The retry
	// is not suppressed — only the noise.
	warned map[string]string
	// dirty records that something was imported since the last refresh, so
	// Refresh can be called once per tick and still cost nothing on the ticks
	// where nothing happened.
	dirty bool
}

// New builds an Importer. refresher may be nil; log may be nil, in which case
// the default logger is used.
func New(st Store, roots Roots, refresher LibraryRefresher, log *slog.Logger) *Importer {
	if log == nil {
		log = slog.Default()
	}
	return &Importer{
		store: st, roots: roots, refresher: refresher,
		log: log, now: time.Now, warned: map[string]string{},
	}
}

// placement is what one import put on disk, as MarkImported and the log line
// need it.
//
// folder is the library_path that gets recorded, and for a show it is the SHOW
// folder rather than the season or the file. That is the identity key every
// future scan matches on, and it is what makes "season 2 arrives six months
// after season 1" converge onto one row: MarkImported's twin reconciliation
// keys on library_path, so the second download is folded into the row the first
// one created (docs/decisions.md D48).
type placement struct {
	folder    string
	size      int64
	episodes  int // 0 for a film, which is how the log line stays what it was
	subtitles int
}

// Import hardlinks one completed download into the library and records it.
//
// The order is load-bearing at two points. The destination folder is not
// created until a source file has been chosen, because a failure before that
// would otherwise leave an empty "Title (Year)/" that the scanner faithfully
// records as a zero-size movie. And the link is made before the database is
// written, because a row claiming a file that is not there is worse than a file
// with no row — the file is found by the next scan, the row never heals itself.
//
// Since T93 the middle of it branches on the row's media type: one file for a
// film, every episode in the download for a show. Everything around that branch
// is shared, because none of it differs — which library root, what a content
// path means, when the folder may be created, that the link precedes the row,
// and that MarkImported is told a FOLDER.
func (im *Importer) Import(ctx context.Context, t torrent.Torrent, d store.Download) (store.Movie, error) {
	fail := func(err error) (store.Movie, error) {
		return store.Movie{}, fmt.Errorf("import %s: %w", d.TorrentHash, err)
	}

	movie, err := im.store.GetMovie(ctx, d.MovieID)
	if err != nil {
		return fail(err)
	}

	root, err := im.roots.For(movie.MediaType)
	if err != nil {
		return fail(err)
	}

	// ContentPath is a path curator can open, by contract: the backend that has
	// a filesystem of its own translates before it answers (docs/decisions.md
	// D22). This package used to hold that translation, from a time when there
	// was one backend and this was the only place that knew both sides.
	src := strings.TrimSpace(t.ContentPath)
	if src == "" {
		return fail(errors.New("the torrent client reported no content path for this torrent"))
	}

	var placed placement
	if movie.MediaType == store.MediaTypeTV {
		placed, err = im.placeEpisodes(root, movie, src)
	} else {
		placed, err = im.placeFeature(root, movie, src, d.TorrentHash)
	}
	if err != nil {
		// Wrapped, not replaced: the caller tests for library.ErrNoVideo — and
		// for a show library.ErrNoEpisode — to decide that this is not a failed
		// download.
		return fail(err)
	}

	// library_path is the FOLDER: it is the scanner's identity key, and a row
	// holding the .mkv path is a row no future scan would match.
	//
	// **imported_at keeps the FIRST moment, for a show as well as for a film,
	// and that is a decision rather than an oversight.** MarkImported writes
	// `imported_at = COALESCE(imported_at, ?)`, so season 2 arriving six months
	// later does not move it. The column answers "when did this title enter the
	// library", the same question for both media types, and season 1 is the
	// honest answer to it — the show has been there since then. Making it
	// "when did anything about this title last change" would break the film
	// meaning it already has (a re-added torrent must not restamp a film that
	// was imported a year ago) to approximate something the database can already
	// answer exactly: every grab is its own `downloads` row with its own
	// timestamp, and the episode files carry their own mtimes.
	saved, err := im.store.MarkImported(ctx, d.TorrentHash, placed.folder, placed.size, im.now().UTC())
	if err != nil {
		// The link stays where it is. It is a fact on disk, the next tick will
		// re-link onto it harmlessly (os.SameFile is success), and removing it
		// here would delete the library's only copy if the write actually landed.
		return fail(err)
	}

	im.mu.Lock()
	im.dirty = true
	im.mu.Unlock()

	attrs := []any{"hash", d.TorrentHash, "title", saved.Title, "year", saved.Year,
		"library_path", placed.folder, "size", placed.size}
	if placed.episodes > 0 {
		attrs = append(attrs, "episodes", placed.episodes)
	}
	attrs = append(attrs, "subtitles", placed.subtitles)
	im.log.Info("imported", attrs...)
	return saved, nil
}

// placeFeature is the film half, and it is exactly what the whole of Import was
// before television: the largest qualifying video, hardlinked into
// "Title (Year)/Title (Year).ext" with its subtitles beside it.
func (im *Importer) placeFeature(root string, movie store.Movie, src, hash string) (placement, error) {
	// The untrusted step. movies.title came from a client via POST
	// /api/downloads, and this is where it becomes a path.
	folder, err := library.DestFolder(movie.Title, movie.Year)
	if err != nil {
		return placement{}, err
	}

	feature, err := library.FindFeature(src, library.FeatureOpts{})
	if err != nil {
		return placement{}, err
	}

	name, err := library.DestName(movie.Title, movie.Year, filepath.Ext(feature.Path))
	if err != nil {
		return placement{}, err
	}

	destDir := filepath.Join(root, folder)
	// Belt and braces over DestFolder's rejection of separators: whatever the
	// naming rules become, the resolved destination has to be inside the library
	// or nothing is created.
	if err := library.AssertInside(root, destDir); err != nil {
		return placement{}, err
	}

	if err := os.MkdirAll(destDir, destDirMode); err != nil {
		return placement{}, err
	}

	dest := filepath.Join(destDir, name)
	if err := library.Link(feature.Path, dest); err != nil {
		return placement{}, err
	}

	if feature.Others > 0 {
		// A double feature. This USED to be how a season pack in the movie
		// category announced itself too — one file imported, the rest reported
		// in a log line nobody reads — and that is the bug T93 closed: a show is
		// now filed as a show because the ROW says so, not rescued from this
		// warning afterwards.
		im.log.Warn("content path held more than one video; imported the largest",
			"hash", hash, "chose", feature.Path, "others", feature.Others)
	}

	// After the feature and before the row, so that a folder is never recorded
	// as imported while it is still missing half of what came with it — and so
	// that a failure here cannot reach the caller. See linkSidecars.
	sidecars := im.findSidecars(src, feature.Path)
	subtitles := im.linkSidecars(root, destDir, name, sidecars, map[string]string{})

	return placement{folder: destDir, size: feature.Size, subtitles: subtitles}, nil
}

// placeEpisodes is the television half: EVERY episode in the download, each
// hardlinked to "Show (Year)/Season NN/Show (Year) - SxxEyy.ext".
//
// **It is atomic in intent: the first failure stops it and is returned.** The
// alternative — link what can be linked, record the show, warn about the rest —
// was rejected because it turns the one thing library.Link exists to refuse
// into a log line: a DIFFERENT file already at a destination is a conflicting
// release, and "imported" with a warning buried in it is how a season ends up
// half somebody else's. Stopping loses nothing. The links already made are
// correct and stay (os.SameFile makes the retry a no-op), D14 retries every
// tick, and the moment the conflict is resolved the rest lands. What the
// operator gets meanwhile is one error naming the exact path, which is the
// point.
//
// The two-phase shape — name and check every destination, then create and link
// — is Import's ordering rule applied per file. A pack whose title cannot be a
// folder name, or one episode of which will not resolve inside the root,
// creates NOTHING, rather than three season folders and then a refusal.
func (im *Importer) placeEpisodes(root string, movie store.Movie, src string) (placement, error) {
	// The untrusted step, exactly as it is for a film: ShowFolder is DestFolder,
	// so D9's colon rule and the path-separator refusal are one implementation
	// for both halves of the library.
	folder, err := library.ShowFolder(movie.Title, movie.Year)
	if err != nil {
		return placement{}, err
	}
	showDir := filepath.Join(root, folder)
	if err := library.AssertInside(root, showDir); err != nil {
		return placement{}, err
	}

	// ErrNoVideo and ErrNoEpisode both come back from here, and NEITHER is a
	// failed download. ErrNoVideo has never been one — the torrent fetched what
	// it advertised — and ErrNoEpisode is the same fact with the blame moved:
	// the bytes are there and the NAMES are the problem, which a rename in the
	// download folder fixes and a retry then picks up. The row stays `completed`
	// so the next tick tries again, and nothing here marks it `failed`.
	episodes, err := library.FindEpisodes(src, library.FeatureOpts{})
	if err != nil {
		return placement{}, err
	}

	// Phase one: every name, and every containment check. Nothing exists on disk
	// yet.
	//
	// **Two files can claim one episode, and this is where that is settled.**
	// FindEpisodes returns a repack beside the original deliberately — "which of
	// two files for one episode to keep is a decision about what is in the
	// library, and it is not this function's to make silently" — so it is made
	// here, out loud. The larger wins, which is FindFeature's own rule for the
	// same question about a film, and the loser is reported rather than dropped
	// in silence. Letting both through instead would link one and then have
	// library.Link refuse the other as "a different file is already there",
	// which fails the whole import over a conflict curator created in it and
	// blames a file it had just written.
	type placedEpisode struct {
		source string
		dir    string
		name   string
		size   int64
	}
	planned := make([]placedEpisode, 0, len(episodes))
	at := make(map[string]int, len(episodes))
	for _, episode := range episodes {
		name, err := library.EpisodeName(
			movie.Title, movie.Year, episode.Season, episode.Episode, filepath.Ext(episode.Path))
		if err != nil {
			return placement{}, err
		}
		seasonDir := filepath.Join(showDir, library.SeasonFolder(episode.Season))
		dest := filepath.Join(seasonDir, name)
		if err := library.AssertInside(root, dest); err != nil {
			return placement{}, err
		}

		candidate := placedEpisode{source: episode.Path, dir: seasonDir, name: name, size: episode.Size}
		i, clash := at[dest]
		if !clash {
			at[dest] = len(planned)
			planned = append(planned, candidate)
			continue
		}
		kept, skipped := planned[i], candidate
		if candidate.size > planned[i].size {
			kept, skipped = candidate, planned[i]
			planned[i] = candidate
		}
		im.log.Warn("two files in this download are the same episode; kept the larger",
			"episode", name, "kept", kept.source, "skipped", skipped.source)
	}

	// Phase two: create and link. A destination that is already our own link is
	// success and a different file there is an error — library.Link's contract,
	// which is what makes re-grabbing a season idempotent and a conflicting
	// release loud (docs/decisions.md D8, per file since T93).
	//
	// The sidecar lookup is cached by the directory it reads, because
	// FindSidecars' answer depends on the content path and on the episode's
	// DIRECTORY and on nothing else. Ten episodes in one folder therefore ask
	// once — which matters most when the answer is a failure, since D14 retries
	// every tick and ten identical warnings per tick for ever is how a log stops
	// being read.
	sidecarsByDir := make(map[string][]string, 2)
	taken := make(map[string]string, len(planned))
	subtitles := 0
	var linked int64
	for _, episode := range planned {
		if err := os.MkdirAll(episode.dir, destDirMode); err != nil {
			return placement{}, err
		}
		linked += episode.size
		if err := library.Link(episode.source, filepath.Join(episode.dir, episode.name)); err != nil {
			return placement{}, err
		}

		dir := filepath.Dir(episode.source)
		found, asked := sidecarsByDir[dir]
		if !asked {
			found = im.findSidecars(src, episode.source)
			sidecarsByDir[dir] = found
		}
		subtitles += im.linkSidecars(root, episode.dir, episode.name,
			episodeSidecars(found, episode.source, len(planned) == 1), taken)
	}

	// What a subtitle rule that refuses to guess costs, said out loud once per
	// import rather than swallowed. A season pack whose subtitles are named
	// "1.srt", "2.srt" … leaves every one of them here, and a person who wants
	// them can see that from the log instead of from a folder they happened to
	// open. They are still in the download, because D8 never removes a source.
	//
	// Only when there was something to be ambiguous BETWEEN. With one episode
	// nothing is filtered out at all, so anything missing was refused rather
	// than unmatched — and linkSidecars has already said so, with the reason.
	if left := unfiled(sidecarsByDir, taken); left > 0 && len(planned) > 1 {
		im.log.Warn("subtitles in this download name no episode, so they were left where they are",
			"library_path", showDir, "left", left, "linked", subtitles)
	}

	return placement{
		folder:    showDir,
		size:      im.showSize(showDir, linked),
		episodes:  len(planned),
		subtitles: subtitles,
	}, nil
}

// unfiled counts the subtitles a download shipped that no episode claimed.
//
// taken is keyed by destination and holds the SOURCE that landed there, so its
// values are exactly what was linked; everything found and not in that set was
// either unmatched or refused, and both are the same fact to somebody looking
// for a missing subtitle.
func unfiled(found map[string][]string, taken map[string]string) int {
	linked := make(map[string]struct{}, len(taken))
	for _, src := range taken {
		linked[src] = struct{}{}
	}

	left := 0
	seen := make(map[string]struct{}, len(taken))
	for _, sidecars := range found {
		for _, src := range sidecars {
			if _, already := seen[src]; already {
				continue
			}
			seen[src] = struct{}{}
			if _, ok := linked[src]; !ok {
				left++
			}
		}
	}
	return left
}

// showSize is the size recorded for a show, and it is the size of the WHOLE
// show folder rather than of what this import just placed.
//
// **This is the decision T93 was asked to settle rather than fall into.**
// MarkImported writes `size_bytes = ?`, so passing the season that just arrived
// would overwrite the season already there with a smaller number — a row
// claiming 12 GB for a show holding 40, until somebody happens to run a scan.
// Two other things read that column and both would be wrong: the Deletion
// report tells a person how much disk removing the show frees, and the UI shows
// it. Re-reading the folder costs one directory walk with no file reads, and it
// produces EXACTLY the number library.ScanShows would write for the same folder
// — same FindEpisodes, same floor — so the row and the next scan never
// disagree in the first place.
//
// The alternative that looks tidier, teaching MarkImported to add rather than
// assign, is wrong for a reason worth writing down: D14 retries an import on
// every tick, and an accumulating write would grow the row by a season every
// ten seconds.
//
// A failure to re-read falls back to placed, the bytes this import itself put
// there, and warns. An accounting number is not worth failing an import that is
// already on disk.
func (im *Importer) showSize(showDir string, placed int64) int64 {
	whole, err := library.FindEpisodes(showDir, library.FeatureOpts{})
	if err != nil {
		im.log.Warn("could not measure the show folder; recorded only what this import added, and the next scan will correct it",
			"library_path", showDir, "size", placed, "err", err)
		return placed
	}

	var sum int64
	for _, episode := range whole {
		sum += episode.Size
	}
	return sum
}

// findSidecars is FindSidecars with its error turned into a warning, because
// nothing about a subtitle may fail an import. See linkSidecars.
func (im *Importer) findSidecars(contentPath, videoPath string) []string {
	sidecars, err := library.FindSidecars(contentPath, videoPath)
	if err != nil {
		im.log.Warn("could not look for subtitles; importing without them",
			"content_path", contentPath, "err", err)
		return nil
	}
	return sidecars
}

// episodeSidecars is which of a download's subtitles belong to ONE episode.
//
// A film has one video, so every subtitle beside it is its own. A season pack
// has ten, and the same list read that way would give every episode every
// subtitle — first-wins by sorted name, which files episode 7's dialogue under
// episode 1. So the rule changes with the count, and both halves of it are
// deliberate:
//
//   - **One episode in the download: the film's rule, unchanged.** A
//     single-episode release folder shipping `Subs/2_English.srt` names nothing,
//     and it does not need to: there is exactly one thing it can belong to.
//   - **More than one: a subtitle must name its own episode.** ParseEpisode on
//     the subtitle's own file name has to agree with the episode's season and
//     number, which is how every release that ships per-episode subtitles names
//     them. One that does not is left in the download rather than filed by
//     guess — the guess is what puts the wrong words on screen, and the file is
//     still there for somebody to place by hand.
//
// It is a function rather than a method because it decides nothing and reads
// nothing: it filters a list the caller already has.
func episodeSidecars(sidecars []string, episodePath string, only bool) []string {
	if only {
		return sidecars
	}

	season, episode, ok := library.ParseEpisode(filepath.Base(episodePath))
	if !ok {
		// Unreachable: FindEpisodes only returns files whose names parsed. Kept
		// because the alternative to a guard here is silently giving one episode
		// every subtitle in the pack.
		return nil
	}

	matched := make([]string, 0, len(sidecars))
	for _, sidecar := range sidecars {
		s, e, ok := library.ParseEpisode(filepath.Base(sidecar))
		if ok && s == season && e == episode {
			matched = append(matched, sidecar)
		}
	}
	return matched
}

// linkSidecars carries subtitles into the library beside the video they belong
// to, named off it, and returns how many landed.
//
// **Nothing in here can fail an import, and that is the whole of its error
// handling.** The video is the point and a subtitle is a courtesy: an import
// that failed because of a bad `.srt` would leave a film out of the library over
// a file nobody would miss, which is the worst trade available. So every failure
// is a Warn and the import stands regardless — which is why this returns a
// count and no error, in the shape TryImport uses to say the same thing about a
// tick.
//
// The rules are the video's rules for the video's reasons: library.Link, so
// a hardlink and never a move, a copy over or a delete of the source
// (docs/decisions.md D8); and library.AssertInside on the destination.
//
// **AssertInside is what makes the closed-table claim true rather than
// assumed.** SidecarName builds the destination out of the video's own name,
// an ISO code from library's table and a known extension, so no part of the
// filename a release group chose reaches the path — but that is a property of
// the code as it stands today, and it is worth one syscall-free check to keep it
// a property rather than a habit.
//
// taken is the names claimed so far, keyed by FULL destination path rather than
// by name: a season pack writes into several season folders in one import, and
// two episodes may legitimately want "…S01E01.en.srt" and "…S02E01.en.srt".
// Passed in rather than made here so one import shares one map.
func (im *Importer) linkSidecars(root, destDir, videoName string, sidecars []string, taken map[string]string) int {
	linked := 0
	for _, src := range sidecars {
		name, err := library.SidecarName(videoName, src)
		if err != nil {
			im.log.Warn("skipped a subtitle: its library name could not be built", "src", src, "err", err)
			continue
		}

		dest := filepath.Join(destDir, name)
		// Two sidecars normalising to one name must not race library.Link for it.
		// Link would refuse the second anyway — a different file already there is
		// never an overwrite — but it would refuse it as an error, and "two
		// English subtitles arrived and one was kept" is not one.
		if first, clash := taken[dest]; clash {
			im.log.Warn("two subtitles want the same name in the library; kept the first",
				"name", name, "kept", first, "skipped", src)
			continue
		}
		if err := library.AssertInside(root, dest); err != nil {
			im.log.Warn("skipped a subtitle: its destination is outside the library", "src", src, "err", err)
			continue
		}
		if err := library.Link(src, dest); err != nil {
			im.log.Warn("could not link a subtitle; importing without it",
				"src", src, "dest", dest, "err", err)
			continue
		}

		taken[dest] = src
		linked++
	}
	return linked
}

// TryImport is Import for the poll tick: it returns nothing at all.
//
// That is the point. An import must not be able to fail a tick — the other
// torrents in the same list still need reconciling — and a method with no error
// return puts that in the type rather than in a comment somebody has to obey.
func (im *Importer) TryImport(ctx context.Context, t torrent.Torrent, d store.Download) {
	if _, err := im.Import(ctx, t, d); err != nil {
		im.logFailure(d.TorrentHash, t.Name, err)
		return
	}
	im.mu.Lock()
	delete(im.warned, d.TorrentHash)
	im.mu.Unlock()
}

// Refresh asks the media server to rescan, at most once per call and only when
// something has actually been imported since the last one.
//
// It returns nothing, for the same reason TryImport does: whether a media
// server has noticed yet is a softer fact than whether the file is in the
// library, and letting it fail a tick would trade a real outcome for a cosmetic
// one (docs/decisions.md D15).
//
// A failure clears the flag rather than arming a retry. Best-effort means what
// it says: Jellyfin rescans on its own schedule anyway, so the worst case is
// that the film appears later rather than not at all, and a Jellyfin that is
// down for a day must not produce a warning every ten seconds until it is back.
func (im *Importer) Refresh(ctx context.Context) {
	if im.refresher == nil {
		return
	}

	im.mu.Lock()
	dirty := im.dirty
	im.dirty = false
	im.mu.Unlock()

	if !dirty {
		return
	}
	if err := im.refresher.RefreshLibrary(ctx); err != nil {
		im.log.Warn("jellyfin refresh failed; the import stands and Jellyfin will find the file on its own schedule", "err", err)
	}
}

// logFailure reports an import failure once per distinct message per torrent.
//
// The mutex is not decoration: the poller's Run is one goroutine and the manual
// import endpoint is another.
func (im *Importer) logFailure(hash, name string, err error) {
	message := err.Error()

	im.mu.Lock()
	previous, seen := im.warned[hash]
	im.warned[hash] = message
	im.mu.Unlock()

	if seen && previous == message {
		return
	}
	im.log.Warn("import failed", "hash", hash, "name", name, "err", err)
}

// RemoveFromLibrary deletes a title's folder and the hardlinks inside it.
//
// It lives here rather than in the delete service because this package created
// that folder, and because it is the one place that holds the roots — keeping
// them in a single place is what makes the containment check meaningful rather
// than something a caller could pass the wrong value to.
//
// **mediaType is required, and it is the whole of what T93 fixed here.** This
// took only a path and checked it against LIBRARY_MOVIES, so deleting a show
// answered ErrOutsideRoot — which D19's caller reads as "there is nothing of
// ours in there to remove", and it then took the rows and left the entire show
// on disk. Rows gone, files orphaned, and a Warn in a log nobody reads before
// clicking delete. Going through Roots.For is what makes the answer depend on
// where the title actually lives.
//
// The refusals For returns stay correct here for the reason its own comment
// gives: an unset LIBRARY_TV or an unrecognised media type wraps
// library.ErrOutsideRoot, so the caller concludes "nothing of ours" — which
// with no television root configured is exactly true — and the row stays
// deletable rather than becoming permanently stuck.
//
// The bytes do not go away when this returns. A hardlink is one of two names for
// an inode, so removing ours frees nothing until the download copy is gone too;
// that is qBittorrent's to delete, and the caller asks it (docs/decisions.md D19).
func (im *Importer) RemoveFromLibrary(mediaType, libraryPath string) error {
	if strings.TrimSpace(libraryPath) == "" {
		return nil // a wanted title was never on disk; nothing to remove
	}
	root, err := im.roots.For(mediaType)
	if err != nil {
		return err
	}
	return library.RemoveMovieFolder(root, libraryPath)
}
