// Package torrserver is a client for a sibling TorrServer ("Matrix" API)
// instance (§9). Endpoint shapes are VERIFY items confirmed against a pinned
// release at build time.
package torrserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// File is one file inside a torrent (from TorrServer's file_stats).
type File struct {
	ID     int    `json:"id"`
	Path   string `json:"path"`
	Length int64  `json:"length"`
}

// Client talks to a TorrServer instance over its internal URL.
type Client struct {
	baseURL string
	http    *http.Client
	mu      sync.RWMutex
	addLink map[string]registeredLink
	added   map[string]string
	adding  map[string]*addLock
	linkTTL time.Duration
}

type registeredLink struct {
	url       string
	expiresAt time.Time
}

// addLock serializes requests for one source without blocking other torrents.
// refs includes the holder and waiters, so idle locks can be discarded.
type addLock struct {
	gate chan struct{}
	refs int
}

// Option configures a Client.
type Option func(*Client)

// WithAddLinkTTL sets how long registered sources remain available for lazy adds.
func WithAddLinkTTL(ttl time.Duration) Option {
	return func(c *Client) {
		if ttl > 0 {
			c.linkTTL = ttl
		}
	}
}

// New builds a client for the given internal base URL (e.g. http://127.0.0.1:8090).
func New(baseURL string, opts ...Option) *Client {
	c := &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		http:    &http.Client{Timeout: 30 * time.Second},
		addLink: make(map[string]registeredLink),
		added:   make(map[string]string),
		adding:  make(map[string]*addLock),
		linkTTL: 6 * time.Hour,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// torrent is the subset of TorrServer's torrent JSON we read.
type torrent struct {
	Hash      string `json:"hash"`
	Title     string `json:"title"`
	FileStats []File `json:"file_stats"`
}

// action posts a body to /torrents and decodes the torrent JSON response.
func (c *Client) action(ctx context.Context, body map[string]any) (*torrent, error) {
	start := time.Now()
	op, _ := body["action"].(string)
	hash, _ := body["hash"].(string)

	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/torrents", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		slog.ErrorContext(ctx, "torrserver request failed", "action", op, "hash", hash, "dur", time.Since(start), "err", err)
		return nil, fmt.Errorf("torrserver request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		slog.ErrorContext(ctx, "torrserver bad status", "action", op, "hash", hash, "status", resp.StatusCode, "dur", time.Since(start))
		return nil, fmt.Errorf("torrserver status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	// "rem" returns an empty body; tolerate that.
	if len(bytes.TrimSpace(data)) == 0 {
		slog.DebugContext(ctx, "torrserver action", "action", op, "hash", hash, "dur", time.Since(start))
		return &torrent{}, nil
	}
	var t torrent
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("torrserver decode: %w", err)
	}
	slog.DebugContext(ctx, "torrserver action", "action", op, "hash", t.Hash, "files", len(t.FileStats), "dur", time.Since(start))
	return &t, nil
}

// Add adds a torrent by magnet/hash/link and returns its hash plus any files
// already known (metadata may still be loading — use EnsureFiles to wait).
func (c *Client) Add(ctx context.Context, link string) (string, []File, error) {
	start := time.Now()
	t, err := c.action(ctx, map[string]any{
		"action":     "add",
		"link":       link,
		"save_to_db": false,
	})
	if err != nil {
		slog.ErrorContext(ctx, "torrserver add failed", "dur", time.Since(start), "err", err)
		return "", nil, err
	}
	if t.Hash == "" {
		return "", nil, fmt.Errorf("torrserver add: no hash returned")
	}
	slog.InfoContext(ctx, "torrserver add", "hash", t.Hash, "files", len(t.FileStats), "dur", time.Since(start))
	return t.Hash, t.FileStats, nil
}

// RegisterAddLink records how to add a hash later. This lets the addon return
// signed /play URLs for obvious single-file episode torrents without creating
// every candidate in TorrServer during stream-list generation.
func (c *Client) RegisterAddLink(hash, link string) {
	hash = strings.ToLower(strings.TrimSpace(hash))
	link = strings.TrimSpace(link)
	if hash == "" || link == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	expiresAt := time.Now().Add(c.linkTTL)
	if previous := c.addLink[hash]; previous.expiresAt.After(expiresAt) {
		expiresAt = previous.expiresAt
	}
	c.addLink[hash] = registeredLink{url: link, expiresAt: expiresAt}
}

// RetainAddLink keeps a registered source usable through a signed URL's expiry.
// Issuing a URL can extend retention, but playback itself does not refresh it.
func (c *Client) RetainAddLink(hash string, until time.Time) {
	key := strings.ToLower(strings.TrimSpace(hash))
	c.mu.Lock()
	defer c.mu.Unlock()
	c.retainLinkLocked(key, until)
	if real := c.added[key]; real != "" {
		c.retainLinkLocked(strings.ToLower(real), until)
	}
}

func (c *Client) retainLinkLocked(key string, until time.Time) {
	if link, ok := c.addLink[key]; ok && until.After(link.expiresAt) {
		link.expiresAt = until
		c.addLink[key] = link
	}
}

// PurgeExpiredLinks bounds both candidate source links and resolved aliases,
// including candidates that were never played or tracked by the sweeper.
func (c *Client) PurgeExpiredLinks(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, link := range c.addLink {
		if !link.expiresAt.After(now) {
			delete(c.addLink, key)
			delete(c.added, key)
		}
	}
}

func (c *Client) lockAdd(ctx context.Context, key string) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	lock := c.adding[key]
	if lock == nil {
		lock = &addLock{gate: make(chan struct{}, 1)}
		c.adding[key] = lock
	}
	lock.refs++
	c.mu.Unlock()
	releaseRef := func() {
		c.mu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(c.adding, key)
		}
		c.mu.Unlock()
	}
	select {
	case <-ctx.Done():
		releaseRef()
		return nil, ctx.Err()
	case lock.gate <- struct{}{}:
		unlock := func() {
			<-lock.gate
			releaseRef()
		}
		if err := ctx.Err(); err != nil {
			unlock()
			return nil, err
		}
		return unlock, nil
	}
}

// EnsureAdded adds a registered token to TorrServer and returns the torrent's
// real infohash. For real-infohash candidates the returned hash equals the
// input. For no-infohash candidates the input is a synthetic token that maps to
// a downloadUrl/magnet; adding it here is how we learn the real infohash (kept
// off the stream-list path). Unregistered tokens are assumed already added and
// returned unchanged.
func (c *Client) EnsureAdded(ctx context.Context, hash string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(hash))
	// Known aliases share a source lock, including a synthetic token and the
	// real hash learned on its first add. Eviction must not race two re-adds.
	c.mu.RLock()
	lockKey := "hash:" + key
	if link := c.addLink[key]; link.url != "" {
		lockKey = "source:" + link.url
	}
	c.mu.RUnlock()
	unlock, err := c.lockAdd(ctx, lockKey)
	if err != nil {
		return "", err
	}
	defer unlock()
	c.mu.RLock()
	real := c.added[key]
	link := c.addLink[key]
	c.mu.RUnlock()
	if !link.expiresAt.After(time.Now()) {
		return hash, nil
	}
	if real != "" {
		if t, err := c.action(ctx, map[string]any{"action": "get", "hash": real}); err == nil && t.Hash != "" {
			return real, nil
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		c.mu.Lock()
		c.invalidateAddedLocked(real)
		c.mu.Unlock()
	}
	if link.url == "" {
		return hash, nil
	}
	addedHash, _, err := c.Add(ctx, link.url)
	if err != nil {
		return "", err
	}
	if addedHash == "" {
		return hash, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	// A purge may have run while Add was in flight. Never resurrect expired
	// links, and inherit the current source horizon without extending it.
	if source, ok := c.addLink[key]; ok && source.expiresAt.After(time.Now()) {
		realKey := strings.ToLower(addedHash)
		alias := c.addLink[realKey]
		if alias.expiresAt.Before(source.expiresAt) {
			c.addLink[realKey] = source
		}
		c.added[key] = addedHash
		c.added[realKey] = addedHash
	}
	return addedHash, nil
}

func (c *Client) invalidateAddedLocked(hash string) {
	for key, real := range c.added {
		if strings.EqualFold(real, hash) {
			delete(c.added, key)
		}
	}
}

// Files returns the current file list for a hash (may be empty if metadata is
// still resolving).
func (c *Client) Files(ctx context.Context, hash string) ([]File, error) {
	t, err := c.action(ctx, map[string]any{"action": "get", "hash": hash})
	if err != nil {
		slog.ErrorContext(ctx, "torrserver files failed", "hash", hash, "err", err)
		return nil, err
	}
	return t.FileStats, nil
}

// EnsureFiles polls Files until the list is non-empty or the deadline passes.
func (c *Client) EnsureFiles(ctx context.Context, hash string, wait time.Duration) ([]File, error) {
	start := time.Now()
	deadline := start.Add(wait)
	attempts := 0
	for {
		attempts++
		files, err := c.Files(ctx, hash)
		if err != nil {
			return nil, err
		}
		if len(files) > 0 {
			slog.DebugContext(ctx, "torrserver ensureFiles resolved", "hash", hash, "files", len(files), "attempts", attempts, "dur", time.Since(start))
			return files, nil
		}
		if time.Now().After(deadline) {
			slog.WarnContext(ctx, "torrserver ensureFiles gave up", "hash", hash, "attempts", attempts, "dur", time.Since(start))
			return files, nil // give up; caller falls back to index 0
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// Remove deletes a torrent from TorrServer (used by the sweeper).
func (c *Client) Remove(ctx context.Context, hash string) error {
	_, err := c.action(ctx, map[string]any{"action": "rem", "hash": hash})
	if err == nil {
		c.mu.Lock()
		c.invalidateAddedLocked(hash)
		c.mu.Unlock()
	}
	return err
}

// Preload asks TorrServer to buffer the start of a file before playback. Best
// effort and short: errors are the caller's to ignore.
func (c *Client) Preload(ctx context.Context, hash string, index int) error {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.preloadURL(hash, index), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		slog.WarnContext(ctx, "torrserver preload failed", "hash", hash, "index", index, "dur", time.Since(start), "err", err)
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	slog.DebugContext(ctx, "torrserver preload", "hash", hash, "index", index, "dur", time.Since(start))
	return nil
}

// StreamURL is the internal URL the /play proxy targets (serves bytes, honors Range).
func (c *Client) StreamURL(hash string, index int) string {
	return fmt.Sprintf("%s/stream/stream?link=%s&index=%d&play",
		c.baseURL, url.QueryEscape(hash), index)
}

func (c *Client) preloadURL(hash string, index int) string {
	return fmt.Sprintf("%s/stream/stream?link=%s&index=%d&preload",
		c.baseURL, url.QueryEscape(hash), index)
}

// BaseURL returns the configured base URL (used to build the proxy target).
func (c *Client) BaseURL() string { return c.baseURL }
