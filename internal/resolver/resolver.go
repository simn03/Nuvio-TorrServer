// Package resolver orchestrates id -> ranked streams (§8): Cinemeta lookup,
// Prowlarr search (movie or series+pack), parse, and rank. TorrServer add and
// signed play URLs are layered on in milestone 5.
package resolver

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"family-torrserver/internal/cinemeta"
	"family-torrserver/internal/config"
	"family-torrserver/internal/prowlarr"
	"family-torrserver/internal/rank"
)

// Resolver wires the metadata and search clients.
type Resolver struct {
	cine *cinemeta.Client
	prow *prowlarr.Client
}

// New builds a Resolver.
func New(cine *cinemeta.Client, prow *prowlarr.Client) *Resolver {
	return &Resolver{cine: cine, prow: prow}
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

// Resolve returns ranked candidates for the given id and user config.
func (r *Resolver) Resolve(ctx context.Context, kind, id string, cfg config.UserConfig) ([]rank.Candidate, error) {
	mid, err := ParseID(kind, id)
	if err != nil {
		return nil, err
	}

	metaKind := "movie"
	if mid.Series {
		metaKind = "series"
	}
	meta, err := r.cine.Get(ctx, metaKind, mid.IMDb)
	if err != nil {
		return nil, fmt.Errorf("cinemeta: %w", err)
	}

	queries, cats := buildQueries(meta, mid)
	results, err := r.searchAll(ctx, queries, cats)
	if err != nil {
		return nil, err
	}

	return rank.Rank(results, cfg), nil
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
func (r *Resolver) searchAll(ctx context.Context, queries []string, cats []int) ([]prowlarr.Result, error) {
	perQuery := make([][]prowlarr.Result, len(queries))
	errs := make([]error, len(queries))
	var wg sync.WaitGroup
	for i, q := range queries {
		wg.Add(1)
		go func(i int, q string) {
			defer wg.Done()
			res, err := r.prow.Search(ctx, q, cats)
			if err != nil {
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
