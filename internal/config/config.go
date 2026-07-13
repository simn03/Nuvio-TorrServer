// Package config defines the per-user UserConfig (Option A, §7): Torrentio-style
// ranking options stored as a JSON blob keyed by the user's token.
package config

import "encoding/json"

// UserConfig holds a user's ranking/filter preferences.
type UserConfig struct {
	Sort             string   `json:"sort"`             // "quality" | "seeders" | "size"
	Resolutions      []string `json:"resolutions"`      // subset of AllResolutions
	ExcludeQualities []string `json:"excludeQualities"` // release types to drop
	PreferHEVC       bool     `json:"preferHEVC"`       // bump x265/HEVC in ranking
	MaxSizeGB        float64  `json:"maxSizeGB"`        // 0 = no cap
	MinSeeders       int      `json:"minSeeders"`       // drop results below this
	MaxResults       int      `json:"maxResults"`       // cap streams returned
	Indexers         []int    `json:"indexers"`         // Prowlarr indexer ids to search; empty = all
}

// Allowed option values (used by defaults, validation, and the config UI).
var (
	AllSorts       = []string{"quality", "seeders", "size"}
	AllResolutions = []string{"4k", "1080p", "720p", "480p", "unknown"}
	AllExcludable  = []string{"cam", "ts", "scr", "hdtc"}
)

// Default returns the seeded defaults from §7.
func Default() UserConfig {
	return UserConfig{
		Sort:             "quality",
		Resolutions:      append([]string(nil), AllResolutions...),
		ExcludeQualities: []string{"cam", "ts"},
		PreferHEVC:       true,
		MaxSizeGB:        0,
		MinSeeders:       3,
		MaxResults:       5,
	}
}

// Marshal serialises the config to JSON for storage.
func Marshal(c UserConfig) ([]byte, error) { return json.Marshal(c) }

// Unmarshal parses stored JSON back into a UserConfig, normalising it so callers
// always receive a valid config even from an older/partial blob.
func Unmarshal(data []byte) (UserConfig, error) {
	c := Default()
	if err := json.Unmarshal(data, &c); err != nil {
		return Default(), err
	}
	return Normalize(c), nil
}

// Normalize clamps/validates a config into a sane, canonical form. Unknown enum
// values fall back to defaults; out-of-range numbers are clamped.
func Normalize(c UserConfig) UserConfig {
	if !contains(AllSorts, c.Sort) {
		c.Sort = "quality"
	}
	c.Resolutions = intersect(AllResolutions, c.Resolutions)
	if len(c.Resolutions) == 0 {
		// An empty set would drop everything; treat as "all enabled".
		c.Resolutions = append([]string(nil), AllResolutions...)
	}
	c.ExcludeQualities = intersect(AllExcludable, c.ExcludeQualities)

	if c.MaxSizeGB < 0 {
		c.MaxSizeGB = 0
	}
	if c.MinSeeders < 0 {
		c.MinSeeders = 0
	}
	if c.MaxResults < 1 {
		c.MaxResults = 1
	}
	if c.MaxResults > 50 {
		c.MaxResults = 50
	}
	c.Indexers = dedupeInts(c.Indexers)
	return c
}

// dedupeInts removes duplicates and non-positive ids, preserving order. A nil/
// empty result means "all indexers".
func dedupeInts(in []int) []int {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[int]bool, len(in))
	var out []int
	for _, v := range in {
		if v > 0 && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func contains(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// intersect returns the members of order that appear in sel, preserving order's
// ordering and dropping anything not in the allowed set (deduped).
func intersect(order, sel []string) []string {
	want := make(map[string]bool, len(sel))
	for _, s := range sel {
		want[s] = true
	}
	var out []string
	for _, s := range order {
		if want[s] {
			out = append(out, s)
		}
	}
	return out
}
