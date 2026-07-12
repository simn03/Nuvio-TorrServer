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
	"net/http"
	"net/url"
	"strings"
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
}

// New builds a client for the given internal base URL (e.g. http://127.0.0.1:8090).
func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		http:    &http.Client{Timeout: 30 * time.Second},
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
		return nil, fmt.Errorf("torrserver request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("torrserver status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	// "rem" returns an empty body; tolerate that.
	if len(bytes.TrimSpace(data)) == 0 {
		return &torrent{}, nil
	}
	var t torrent
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("torrserver decode: %w", err)
	}
	return &t, nil
}

// Add adds a torrent by magnet/hash/link and returns its hash plus any files
// already known (metadata may still be loading — use EnsureFiles to wait).
func (c *Client) Add(ctx context.Context, link string) (string, []File, error) {
	t, err := c.action(ctx, map[string]any{
		"action":     "add",
		"link":       link,
		"save_to_db": false,
	})
	if err != nil {
		return "", nil, err
	}
	if t.Hash == "" {
		return "", nil, fmt.Errorf("torrserver add: no hash returned")
	}
	return t.Hash, t.FileStats, nil
}

// Files returns the current file list for a hash (may be empty if metadata is
// still resolving).
func (c *Client) Files(ctx context.Context, hash string) ([]File, error) {
	t, err := c.action(ctx, map[string]any{"action": "get", "hash": hash})
	if err != nil {
		return nil, err
	}
	return t.FileStats, nil
}

// EnsureFiles polls Files until the list is non-empty or the deadline passes.
func (c *Client) EnsureFiles(ctx context.Context, hash string, wait time.Duration) ([]File, error) {
	deadline := time.Now().Add(wait)
	for {
		files, err := c.Files(ctx, hash)
		if err != nil {
			return nil, err
		}
		if len(files) > 0 {
			return files, nil
		}
		if time.Now().After(deadline) {
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
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.preloadURL(hash, index), nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
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
