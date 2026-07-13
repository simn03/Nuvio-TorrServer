// Package resolver orchestrates id -> ranked streams (§8): Cinemeta lookup,
// Prowlarr search (movie or series+pack), parse, and rank. TorrServer add and
// signed play URLs are layered on in milestone 5.
package resolver

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/simn03/nuvio-p2p-http-addon/internal/cinemeta"
	"github.com/simn03/nuvio-p2p-http-addon/internal/config"
	"github.com/simn03/nuvio-p2p-http-addon/internal/prowlarr"
	"github.com/simn03/nuvio-p2p-http-addon/internal/rank"
	"github.com/simn03/nuvio-p2p-http-addon/internal/torrserver"
)

// Resolver wires the metadata and search clients.
type Resolver struct {
	cine    *cinemeta.Client
	prow    *prowlarr.Client
	ts      *torrserver.Client
	sweeper *torrserver.Sweeper
	preload bool

	// fileWait bounds how long we wait for TorrServer to resolve a torrent's
	// file list before falling back. Overridable in tests.
	fileWait    time.Duration
	concurrency int
}

// New builds a Resolver. ts/sweeper may be nil when only the pure ranking path
// (Candidates) is used, e.g. in tests.
func New(cine *cinemeta.Client, prow *prowlarr.Client, ts *torrserver.Client, sweeper *torrserver.Sweeper, preload bool) *Resolver {
	return &Resolver{
		cine:        cine,
		prow:        prow,
		ts:          ts,
		sweeper:     sweeper,
		preload:     preload,
		fileWait:    8 * time.Second,
		concurrency: 4,
	}
}

// Stream is a playable result: a ranked candidate resolved to a specific
// TorrServer torrent hash and file index.
type Stream struct {
	Candidate rank.Candidate
	Hash      string
	FileIndex int
}

// MediaID is a parsed Stremio stream id.
type MediaID struct {
	IMDb    string
	Season  int
	Episode int
	Series  bool
}

// ParseID parses "tt1234567" (movie) or "tt1234567:S:E" (series).
func ParseID(kind, id string) (MediaID, error) {
	unescaped, err := url.PathUnescape(id)
	if err != nil {
		return MediaID{}, fmt.Errorf("bad id %q: %w", id, err)
	}
	id = unescaped
	parts := strings.Split(id, ":")
	m := MediaID{IMDb: parts[0]}
	if !strings.HasPrefix(m.IMDb, "tt") {
		return MediaID{}, fmt.Errorf("unsupported id %q", id)
	}
	if kind == "series" || len(parts) >= 3 {
		m.Series = true
		if len(parts) >= 3 {
			m.Season, _ = strconv.Atoi(parts[1])
			m.Episode, _ = strconv.Atoi(parts[2])
		}
	}
	return m, nil
}

// Candidates runs the metadata + search + rank pipeline and returns the ranked
// candidates (no TorrServer interaction). This is the pure, network-only path.
func (r *Resolver) Candidates(ctx context.Context, kind, id string, cfg config.UserConfig) ([]rank.Candidate, MediaID, error) {
	total := time.Now()
	mid, err := ParseID(kind, id)
	if err != nil {
		return nil, MediaID{}, err
	}

	metaKind := "movie"
	if mid.Series {
		metaKind = "series"
	}
	step := time.Now()
	meta, err := r.cine.Get(ctx, metaKind, mid.IMDb)
	if err != nil {
		slog.ErrorContext(ctx, "resolve: cinemeta lookup failed", "id", id, "dur", time.Since(step), "err", err)
		return nil, mid, fmt.Errorf("cinemeta: %w", err)
	}
	cinemetaDur := time.Since(step)

	queries, cats := buildQueries(meta, mid)
	step = time.Now()
	results, err := r.searchAll(ctx, queries, cats, cfg.Indexers)
	if err != nil {
		slog.ErrorContext(ctx, "resolve: search failed", "id", id, "queries", queries, "dur", time.Since(step), "err", err)
		return nil, mid, err
	}
	rawResults := len(results)
	results = filterResultsForMedia(results, mid)
	searchDur := time.Since(step)

	step = time.Now()
	candidates := rank.Rank(results, cfg)
	prioritizeExactEpisode(candidates, mid)
	slog.InfoContext(ctx, "resolve: candidates ready",
		"id", id, "title", meta.Name, "queries", queries,
		"raw_results", rawResults, "filtered_results", len(results), "candidates", len(candidates),
		"cinemeta_dur", cinemetaDur, "search_dur", searchDur, "rank_dur", time.Since(step),
		"total_dur", time.Since(total),
	)
	return candidates, mid, nil
}

// Resolve returns playable streams: it ranks candidates, then adds each to
// TorrServer to obtain its hash and pick the correct file (season-pack episode
// selection for series, largest video file for movies). Candidates that can't
// be added are dropped.
func (r *Resolver) Resolve(ctx context.Context, kind, id string, cfg config.UserConfig) ([]Stream, error) {
	start := time.Now()
	candidates, mid, err := r.Candidates(ctx, kind, id, cfg)
	if err != nil {
		return nil, err
	}
	if r.ts == nil {
		return nil, fmt.Errorf("resolver: no TorrServer client configured")
	}

	streams := make([]Stream, len(candidates))
	ok := make([]bool, len(candidates))

	sem := make(chan struct{}, r.concurrency)
	var wg sync.WaitGroup
	for i, c := range candidates {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, c rank.Candidate) {
			defer wg.Done()
			defer func() { <-sem }()
			s, err := r.enrich(ctx, c, mid)
			if err != nil {
				slog.WarnContext(ctx, "resolve: candidate dropped", "title", c.Result.Title, "err", err)
				return
			}
			streams[i] = s
			ok[i] = true
		}(i, c)
	}
	wg.Wait()

	// Preserve ranking order, dropping candidates that failed to resolve.
	out := make([]Stream, 0, len(candidates))
	for i := range streams {
		if ok[i] {
			out = append(out, streams[i])
		}
	}
	r.preloadTop(ctx, out)
	slog.InfoContext(ctx, "resolve: complete", "id", id, "candidates", len(candidates), "streams", len(out), "dur", time.Since(start))
	return out, nil
}

// enrich adds one candidate to TorrServer and selects its file index.
func (r *Resolver) enrich(ctx context.Context, c rank.Candidate, mid MediaID) (Stream, error) {
	start := time.Now()
	link := c.Result.Link()
	if link == "" {
		return Stream{}, fmt.Errorf("no link")
	}
	if hash, ok := lazyPlayableHash(c, mid); ok {
		r.ts.RegisterAddLink(hash, link)
		slog.DebugContext(ctx, "resolve: candidate lazy registered", "title", c.Result.Title, "hash", hash, "file_index", 1, "dur", time.Since(start))
		return Stream{Candidate: c, Hash: hash, FileIndex: 1}, nil
	}
	hash, files, err := r.ts.Add(ctx, link)
	if err != nil {
		return Stream{}, err
	}
	if len(files) == 0 {
		if shouldWaitForFiles(c.Parsed, mid) {
			files, _ = r.ts.EnsureFiles(ctx, hash, r.fileWait)
		} else {
			slog.DebugContext(ctx, "resolve: exact episode release; skipping file wait", "title", c.Result.Title, "hash", hash)
		}
	}

	index := selectFile(files, mid)
	r.sweeper.Touch(hash)
	slog.DebugContext(ctx, "resolve: candidate enriched", "title", c.Result.Title, "hash", hash, "file_index", index, "dur", time.Since(start))
	return Stream{Candidate: c, Hash: hash, FileIndex: index}, nil
}

func lazyPlayableHash(c rank.Candidate, mid MediaID) (string, bool) {
	if shouldWaitForFiles(c.Parsed, mid) {
		return "", false
	}
	hash := prowlarr.NormalizeInfoHash(c.Result.InfoHash)
	return hash, hash != ""
}

func (r *Resolver) preloadTop(ctx context.Context, streams []Stream) {
	if !r.preload || len(streams) == 0 {
		return
	}
	s := streams[0]
	go func() {
		pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := r.ts.EnsureAdded(pctx, s.Hash); err != nil {
			return
		}
		if err := r.ts.Preload(pctx, s.Hash, s.FileIndex); err != nil {
			return
		}
		slog.DebugContext(pctx, "resolve: top stream preloaded", "hash", s.Hash, "file_index", s.FileIndex)
	}()
}

func filterResultsForMedia(results []prowlarr.Result, mid MediaID) []prowlarr.Result {
	if !mid.Series || mid.Episode <= 0 {
		return results
	}
	out := results[:0]
	for _, res := range results {
		p := rank.Parse(res.Title)
		if p.HasEpisode && !episodeMatches(p, mid) {
			continue
		}
		out = append(out, res)
	}
	return out
}

func shouldWaitForFiles(p rank.Parsed, mid MediaID) bool {
	if !mid.Series || mid.Episode <= 0 {
		return true
	}
	return !p.HasEpisode || !episodeMatches(p, mid)
}

func episodeMatches(p rank.Parsed, mid MediaID) bool {
	if !p.HasEpisode || p.Episode != mid.Episode {
		return false
	}
	return !p.HasSeason || mid.Season == 0 || p.Season == mid.Season
}

func prioritizeExactEpisode(candidates []rank.Candidate, mid MediaID) {
	if !mid.Series || mid.Episode <= 0 {
		return
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		iExact := episodeMatches(candidates[i].Parsed, mid)
		jExact := episodeMatches(candidates[j].Parsed, mid)
		return iExact && !jExact
	})
}

// buildQueries produces the search query strings and categories (§8 step 3).
func buildQueries(meta cinemeta.Meta, mid MediaID) ([]string, []int) {
	name := strings.TrimSpace(meta.Name)
	if mid.Series {
		var qs []string
		if mid.Season > 0 && mid.Episode > 0 {
			qs = append(qs, fmt.Sprintf("%s S%02dE%02d", name, mid.Season, mid.Episode))
		}
		if mid.Season > 0 {
			qs = append(qs, fmt.Sprintf("%s S%02d", name, mid.Season)) // season pack
		}
		if len(qs) == 0 {
			qs = append(qs, name)
		}
		return qs, []int{prowlarr.CatTV}
	}

	q := name
	if meta.Year > 0 {
		q = fmt.Sprintf("%s %d", name, meta.Year)
	}
	return []string{q}, []int{prowlarr.CatMovies}
}

// searchAll runs the queries concurrently and merges+dedupes the results. A
// per-query failure is non-fatal (e.g. a slow, broad season-pack query timing
// out shouldn't discard a successful episode query); we only return an error if
// every query fails. Note: errgroup is deliberately NOT used here, because its
// context cancellation on first error would abort the sibling queries too.
func (r *Resolver) searchAll(ctx context.Context, queries []string, cats, indexerIds []int) ([]prowlarr.Result, error) {
	perQuery := make([][]prowlarr.Result, len(queries))
	errs := make([]error, len(queries))
	var wg sync.WaitGroup
	for i, q := range queries {
		wg.Add(1)
		go func(i int, q string) {
			defer wg.Done()
			qStart := time.Now()
			res, err := r.prow.Search(ctx, q, cats, indexerIds)
			if err != nil {
				slog.WarnContext(ctx, "resolve: query failed", "query", q, "dur", time.Since(qStart), "err", err)
				errs[i] = err
				return
			}
			perQuery[i] = res
		}(i, q)
	}
	wg.Wait()

	// Only fail if all queries failed; otherwise proceed with what we have.
	failed := 0
	for _, e := range errs {
		if e != nil {
			failed++
		}
	}
	if failed == len(queries) {
		return nil, fmt.Errorf("prowlarr search: all %d queries failed: %w", failed, firstErr(errs))
	}

	seen := make(map[string]bool)
	var merged []prowlarr.Result
	for _, res := range perQuery {
		for _, item := range res {
			// TorrServer is torrent-only; drop usenet or other protocols.
			if item.Protocol != "" && item.Protocol != "torrent" {
				continue
			}
			key := dedupeKey(item)
			if seen[key] {
				continue
			}
			seen[key] = true
			merged = append(merged, item)
		}
	}
	return merged, nil
}

var videoExts = map[string]bool{
	".mkv": true, ".mp4": true, ".avi": true, ".m4v": true, ".mov": true,
	".ts": true, ".wmv": true, ".flv": true, ".webm": true, ".mpg": true, ".mpeg": true,
}

// selectFile picks the TorrServer file index (1-based id) to stream. For a
// series episode it matches the requested S/E against each file name (season-
// pack selection, §8 step 8); otherwise it picks the largest video file. Falls
// back to index 1 when no file list is available.
func selectFile(files []torrserver.File, mid MediaID) int {
	if len(files) == 0 {
		return 1
	}

	// Collect video files with their sizes.
	type vf struct {
		id   int
		size int64
		name string
	}
	var vids []vf
	for _, f := range files {
		ext := strings.ToLower(path.Ext(f.Path))
		if videoExts[ext] {
			vids = append(vids, vf{id: f.ID, size: f.Length, name: path.Base(f.Path)})
		}
	}
	if len(vids) == 0 {
		return files[0].ID // no recognisable video; best effort
	}

	// Series with a specific episode: find the file whose parsed episode matches.
	if mid.Series && mid.Episode > 0 {
		for _, v := range vids {
			p := rank.Parse(v.name)
			if p.HasEpisode && p.Episode == mid.Episode &&
				(!p.HasSeason || mid.Season == 0 || p.Season == mid.Season) {
				return v.id
			}
		}
	}

	// Otherwise (or no episode match): largest video file.
	best := vids[0]
	for _, v := range vids[1:] {
		if v.size > best.size {
			best = v
		}
	}
	return best.id
}

func firstErr(errs []error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func dedupeKey(r prowlarr.Result) string {
	switch {
	case r.InfoHash != "":
		return "h:" + strings.ToLower(r.InfoHash)
	case r.GUID != "":
		return "g:" + r.GUID
	default:
		return "t:" + strings.ToLower(strings.TrimSpace(r.Title))
	}
}
