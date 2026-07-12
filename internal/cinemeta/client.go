// Package cinemeta resolves IMDb IDs to titles/years via Stremio's public
// Cinemeta service, cached in the store (§8 step 2).
package cinemeta

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"
)

// Cache is the subset of the store this client needs.
type Cache interface {
	CinemetaCacheGet(imdb, kind string) ([]byte, bool)
	CinemetaCacheSet(imdb, kind string, payload []byte, ttl time.Duration) error
}

// Meta is the distilled metadata we cache and use for query building.
type Meta struct {
	Name string `json:"name"`
	Year int    `json:"year"` // 0 if unknown
}

// Client fetches (and caches) Cinemeta metadata.
type Client struct {
	baseURL string
	http    *http.Client
	cache   Cache
	ttl     time.Duration
}

// New builds a Cinemeta client.
func New(baseURL string, cache Cache, ttl time.Duration) *Client {
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 10 * time.Second},
		cache:   cache,
		ttl:     ttl,
	}
}

var yearRe = regexp.MustCompile(`\d{4}`)

// cinemetaResponse mirrors the shape we read: {"meta": {"name": ..., ...}}.
// year/releaseInfo can be a string ("1994", "1994–1998") or a number, so we
// decode leniently and extract the first 4-digit run.
type cinemetaResponse struct {
	Meta struct {
		Name        string          `json:"name"`
		Year        json.RawMessage `json:"year"`
		ReleaseInfo json.RawMessage `json:"releaseInfo"`
	} `json:"meta"`
}

// Get returns metadata for the given kind ("movie"|"series") and IMDb id.
func (c *Client) Get(ctx context.Context, kind, imdb string) (Meta, error) {
	if data, ok := c.cache.CinemetaCacheGet(imdb, kind); ok {
		var m Meta
		if json.Unmarshal(data, &m) == nil {
			return m, nil
		}
	}

	url := fmt.Sprintf("%s/meta/%s/%s.json", c.baseURL, kind, imdb)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Meta{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Meta{}, fmt.Errorf("cinemeta request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Meta{}, fmt.Errorf("cinemeta status %d for %s", resp.StatusCode, imdb)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Meta{}, fmt.Errorf("cinemeta read: %w", err)
	}

	var parsed cinemetaResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return Meta{}, fmt.Errorf("cinemeta decode: %w", err)
	}
	m := Meta{
		Name: parsed.Meta.Name,
		Year: extractYear(parsed.Meta.Year, parsed.Meta.ReleaseInfo),
	}
	if m.Name == "" {
		return Meta{}, fmt.Errorf("cinemeta: empty name for %s", imdb)
	}

	if enc, err := json.Marshal(m); err == nil {
		_ = c.cache.CinemetaCacheSet(imdb, kind, enc, c.ttl)
	}
	return m, nil
}

// extractYear pulls the first 4-digit year from the year/releaseInfo fields,
// each of which may be a JSON string or number.
func extractYear(fields ...json.RawMessage) int {
	for _, raw := range fields {
		if len(raw) == 0 {
			continue
		}
		s := string(raw)
		if len(s) >= 2 && s[0] == '"' {
			var unq string
			if json.Unmarshal(raw, &unq) == nil {
				s = unq
			}
		}
		if match := yearRe.FindString(s); match != "" {
			y := 0
			for _, r := range match {
				y = y*10 + int(r-'0')
			}
			return y
		}
	}
	return 0
}
