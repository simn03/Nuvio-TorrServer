// Package rank turns raw Prowlarr results into a filtered, sorted stream list
// per a user's UserConfig. The release-name parser (§5) lives here behind the
// package boundary so the library choice stays swappable.
package rank

import (
	"regexp"
	"sort"
	"strings"

	"family-torrserver/internal/config"
	"family-torrserver/internal/prowlarr"

	ptn "github.com/middelink/go-parse-torrent-name"
)

// Parsed holds the release attributes we rank/filter on.
type Parsed struct {
	Resolution string // 4k | 1080p | 720p | 480p | unknown
	Quality    string // raw, e.g. "BluRay", "WEB-DL", "CAM"
	Codec      string // raw, e.g. "x265"
	IsHEVC     bool
	Season     int
	Episode    int
	HasSeason  bool
	HasEpisode bool
}

// Candidate is a Prowlarr result together with its parsed attributes.
type Candidate struct {
	Result prowlarr.Result
	Parsed Parsed
}

// resolution ranking, high to low, for the "quality" sort.
var resolutionRank = map[string]int{
	"4k":      4,
	"1080p":   3,
	"720p":    2,
	"480p":    1,
	"unknown": 0,
}

// Supplementary matchers for gaps the library misses (§5): season-only packs
// and space-separated "S01 E05".
var (
	seasonEpisodeSpaceRe = regexp.MustCompile(`(?i)\bS(\d{1,2})\s*E(\d{1,3})\b`)
	seasonOnlyRe         = regexp.MustCompile(`(?i)\bS(\d{1,2})\b(?:[^E\d]|$)`)
	seasonWordRe         = regexp.MustCompile(`(?i)\bSeason\s*(\d{1,2})\b`)
)

// Parse extracts attributes from a release title.
func Parse(title string) Parsed {
	var p Parsed
	if info, err := ptn.Parse(title); err == nil {
		p.Resolution = normalizeResolution(info.Resolution)
		p.Quality = info.Quality
		p.Codec = info.Codec
		if info.Season > 0 {
			p.Season, p.HasSeason = info.Season, true
		}
		if info.Episode > 0 {
			p.Episode, p.HasEpisode = info.Episode, true
		}
	} else {
		p.Resolution = "unknown"
	}

	// Supplementary season/episode detection for library gaps.
	if !p.HasSeason || !p.HasEpisode {
		if m := seasonEpisodeSpaceRe.FindStringSubmatch(title); m != nil {
			p.Season, p.HasSeason = atoi(m[1]), true
			p.Episode, p.HasEpisode = atoi(m[2]), true
		}
	}
	if !p.HasSeason {
		if m := seasonOnlyRe.FindStringSubmatch(title); m != nil {
			p.Season, p.HasSeason = atoi(m[1]), true
		} else if m := seasonWordRe.FindStringSubmatch(title); m != nil {
			p.Season, p.HasSeason = atoi(m[1]), true
		}
	}

	p.IsHEVC = detectHEVC(title, p.Codec)
	return p
}

func normalizeResolution(r string) string {
	switch strings.ToLower(strings.TrimSpace(r)) {
	case "2160p", "4k", "uhd":
		return "4k"
	case "1080p":
		return "1080p"
	case "720p":
		return "720p"
	case "480p", "576p":
		return "480p"
	default:
		return "unknown"
	}
}

func detectHEVC(title, codec string) bool {
	c := strings.ToLower(codec)
	if strings.Contains(c, "x265") || strings.Contains(c, "hevc") || strings.Contains(c, "h265") {
		return true
	}
	t := strings.ToLower(title)
	return strings.Contains(t, "x265") || strings.Contains(t, "hevc") || strings.Contains(t, "h.265") || strings.Contains(t, "h265")
}

// Rank parses, filters, and sorts results per cfg, truncating to MaxResults.
func Rank(results []prowlarr.Result, cfg config.UserConfig) []Candidate {
	cfg = config.Normalize(cfg)
	resAllowed := toSet(cfg.Resolutions)
	excluded := toSet(lowerAll(cfg.ExcludeQualities))
	maxBytes := int64(cfg.MaxSizeGB * 1024 * 1024 * 1024)

	var out []Candidate
	for _, r := range results {
		p := Parse(r.Title)

		if r.Seeders < cfg.MinSeeders {
			continue
		}
		if !resAllowed[p.Resolution] {
			continue
		}
		if excluded[strings.ToLower(p.Quality)] {
			continue
		}
		if maxBytes > 0 && r.Size > maxBytes {
			continue
		}
		out = append(out, Candidate{Result: r, Parsed: p})
	}

	sortCandidates(out, cfg)

	if len(out) > cfg.MaxResults {
		out = out[:cfg.MaxResults]
	}
	return out
}

func sortCandidates(c []Candidate, cfg config.UserConfig) {
	less := func(i, j int) bool {
		a, b := c[i], c[j]
		switch cfg.Sort {
		case "seeders":
			if a.Result.Seeders != b.Result.Seeders {
				return a.Result.Seeders > b.Result.Seeders
			}
		case "size":
			if a.Result.Size != b.Result.Size {
				return a.Result.Size > b.Result.Size
			}
		default: // "quality": resolution, then seeders
			ar, br := resolutionRank[a.Parsed.Resolution], resolutionRank[b.Parsed.Resolution]
			if ar != br {
				return ar > br
			}
			if a.Result.Seeders != b.Result.Seeders {
				return a.Result.Seeders > b.Result.Seeders
			}
		}
		// PreferHEVC as a tiebreak/boost.
		if cfg.PreferHEVC && a.Parsed.IsHEVC != b.Parsed.IsHEVC {
			return a.Parsed.IsHEVC
		}
		// Stable final tiebreak.
		return a.Result.Seeders > b.Result.Seeders
	}
	sort.SliceStable(c, less)
}

func toSet(items []string) map[string]bool {
	m := make(map[string]bool, len(items))
	for _, s := range items {
		m[s] = true
	}
	return m
}

func lowerAll(items []string) []string {
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = strings.ToLower(s)
	}
	return out
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
	}
	return n
}
