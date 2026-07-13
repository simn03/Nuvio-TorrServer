// Package prowlarr searches the operator's Prowlarr instance and caches results
// (§8 step 4). Field names are VERIFY items — decoding is lenient.
package prowlarr

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Category constants (Newznab/Torznab).
const (
	CatMovies = 2000
	CatTV     = 5000
)

// Cache is the subset of the store this client needs.
type Cache interface {
	ProwlarrCacheGet(key string) ([]byte, bool)
	ProwlarrCacheSet(key string, payload []byte, ttl time.Duration) error
}

// Result is a single search hit, distilled from the Prowlarr release JSON.
// Verified against a live Prowlarr: field names match, but magnetUrl/infoHash/
// downloadUrl are each present on only ~half of results — the union is complete,
// so Link() always resolves.
type Result struct {
	Title       string `json:"title"`
	Size        int64  `json:"size"`
	Seeders     int    `json:"seeders"`
	DownloadURL string `json:"downloadUrl"`
	MagnetURL   string `json:"magnetUrl"`
	InfoHash    string `json:"infoHash"`
	GUID        string `json:"guid"`
	Protocol    string `json:"protocol"` // "torrent" | "usenet"
	Indexer     string `json:"indexer"`  // source indexer name, for display
	IndexerID   int    `json:"indexerId"`
}

// Link returns the best add-link for TorrServer. Order matters: a magnet or a
// magnet synthesised from the infohash lets TorrServer add directly, whereas
// downloadUrl is a Prowlarr-proxied .torrent URL (embeds Prowlarr's API key and
// requires TorrServer to fetch through Prowlarr — which fails if Prowlarr uses
// an untrusted TLS cert), so it's the last resort.
func (r Result) Link() string {
	if r.MagnetURL != "" {
		return r.MagnetURL
	}
	if ih := NormalizeInfoHash(r.InfoHash); ih != "" {
		return "magnet:?xt=urn:btih:" + ih
	}
	if r.DownloadURL != "" {
		return r.DownloadURL
	}
	return ""
}

// NormalizeInfoHash returns a valid BitTorrent v1 infohash (40 lowercase hex or
// 32 base32) or "" if it can't. Some Prowlarr indexers return the infohash
// double-hex-encoded (80 hex chars that decode to the real 40-hex string); this
// unwraps that case.
func NormalizeInfoHash(s string) string {
	s = strings.TrimSpace(s)
	switch {
	case len(s) == 40 && isHex(s):
		return strings.ToLower(s)
	case len(s) == 32 && isBase32(s):
		return strings.ToUpper(s)
	case len(s) == 80 && isHex(s):
		// Hex-of-ascii: decode once and re-check for a 40-hex string.
		if b, err := hex.DecodeString(s); err == nil {
			inner := strings.TrimSpace(string(b))
			if len(inner) == 40 && isHex(inner) {
				return strings.ToLower(inner)
			}
		}
	}
	return ""
}

func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func isBase32(s string) bool {
	for _, c := range s {
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '2' && c <= '7')) {
			return false
		}
	}
	return true
}

// Client queries Prowlarr's search API.
type Client struct {
	baseURL string
	apiKey  string
	http    *http.Client
	cache   Cache
	ttl     time.Duration
}

// New builds a Prowlarr client. insecureTLS skips certificate verification,
// intended for self-hosted Prowlarr behind a private/internal CA (§4).
func New(baseURL, apiKey string, cache Cache, ttl time.Duration, insecureTLS bool) *Client {
	hc := &http.Client{Timeout: 20 * time.Second}
	if insecureTLS {
		hc.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		}
	}
	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		apiKey:  apiKey,
		http:    hc,
		cache:   cache,
		ttl:     ttl,
	}
}

// prowlarrRelease is the lenient decode target; Prowlarr returns more fields
// than we keep, and magnet/infohash naming varies by version.
type prowlarrRelease struct {
	Title       string `json:"title"`
	Size        int64  `json:"size"`
	Seeders     int    `json:"seeders"`
	DownloadURL string `json:"downloadUrl"`
	MagnetURL   string `json:"magnetUrl"`
	InfoHash    string `json:"infoHash"`
	GUID        string `json:"guid"`
	Protocol    string `json:"protocol"`
	Indexer     string `json:"indexer"`
	IndexerID   int    `json:"indexerId"`
}

// Indexer is a configured Prowlarr indexer (for the per-user selection UI).
type Indexer struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Enable   bool   `json:"enable"`
	Protocol string `json:"protocol"`
}

// Search runs a query for the given categories, using the cache when warm. When
// indexerIds is non-empty the search is limited to those indexers; otherwise
// Prowlarr queries all enabled indexers.
func (c *Client) Search(ctx context.Context, query string, categories, indexerIds []int) ([]Result, error) {
	key := cacheKey(query, categories, indexerIds)
	if data, ok := c.cache.ProwlarrCacheGet(key); ok {
		var cached []Result
		if json.Unmarshal(data, &cached) == nil {
			return cached, nil
		}
	}

	u, err := url.Parse(c.baseURL + "/api/v1/search")
	if err != nil {
		return nil, err
	}
	q := u.Query()
	q.Set("query", query)
	q.Set("type", "search")
	for _, cat := range categories {
		q.Add("categories", strconv.Itoa(cat))
	}
	for _, id := range indexerIds {
		q.Add("indexerIds", strconv.Itoa(id))
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prowlarr request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prowlarr status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("prowlarr read: %w", err)
	}

	var raw []prowlarrRelease
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("prowlarr decode: %w", err)
	}
	results := make([]Result, 0, len(raw))
	for _, r := range raw {
		results = append(results, Result(r))
	}

	if enc, err := json.Marshal(results); err == nil {
		_ = c.cache.ProwlarrCacheSet(key, enc, c.ttl)
	}
	return results, nil
}

// cacheKey hashes the normalised query + categories + selected indexer ids, so
// different indexer selections cache independently.
func cacheKey(query string, categories, indexerIds []int) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(strings.TrimSpace(query)))
	b.WriteByte('|')
	for _, c := range categories {
		b.WriteString(strconv.Itoa(c))
		b.WriteByte(',')
	}
	b.WriteByte('|')
	for _, id := range sortedCopy(indexerIds) {
		b.WriteString(strconv.Itoa(id))
		b.WriteByte(',')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

func sortedCopy(in []int) []int {
	out := append([]int(nil), in...)
	sort.Ints(out)
	return out
}

// Indexers lists Prowlarr's configured indexers (for the per-user selection UI).
func (c *Client) Indexers(ctx context.Context) ([]Indexer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/v1/indexer", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", c.apiKey)
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prowlarr indexers: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("prowlarr indexers status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var idx []Indexer
	if err := json.Unmarshal(body, &idx); err != nil {
		return nil, fmt.Errorf("prowlarr indexers decode: %w", err)
	}
	return idx, nil
}
