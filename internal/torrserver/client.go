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
	addLink map[string]string
}

// New builds a client for the given internal base URL (e.g. http://127.0.0.1:8090).
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		http:    &http.Client{Timeout: 30 * time.Second},
		addLink: make(map[string]string),
	}
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
	c.addLink[hash] = link
}

// EnsureAdded adds a registered token to TorrServer and returns the torrent's
// real infohash. For real-infohash candidates the returned hash equals the
// input. For no-infohash candidates the input is a synthetic token that maps to
// a downloadUrl/magnet; adding it here is how we learn the real infohash (kept
// off the stream-list path). Unregistered tokens are assumed already added and
// returned unchanged.
func (c *Client) EnsureAdded(ctx context.Context, hash string) (string, error) {
	key := strings.ToLower(strings.TrimSpace(hash))
	c.mu.RLock()
	link := c.addLink[key]
	c.mu.RUnlock()
	if link == "" {
		return hash, nil
	}
	addedHash, _, err := c.Add(ctx, link)
	if err != nil {
		return "", err
	}
	if addedHash == "" {
		return hash, nil
	}
	if !strings.EqualFold(addedHash, key) {
		// Synthetic token resolved to its real infohash; register the real hash
		// too so repeat plays and the sweeper resolve consistently.
		c.RegisterAddLink(addedHash, link)
	}
	return addedHash, nil
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
