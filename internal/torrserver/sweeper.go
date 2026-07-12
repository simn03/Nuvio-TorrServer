package torrserver

import (
	"context"
	"log"
	"sync"
	"time"
)

// Sweeper tracks per-hash last-access and removes torrents idle beyond the TTL
// (§9). Torrents are added with save_to_db:false (in-engine only), so we can't
// enumerate them from TorrServer — we track the hashes we added ourselves and
// remove by hash.
type Sweeper struct {
	client   *Client
	ttl      time.Duration
	mu       sync.Mutex
	lastSeen map[string]time.Time
}

// NewSweeper builds a Sweeper.
func NewSweeper(client *Client, ttl time.Duration) *Sweeper {
	return &Sweeper{
		client:   client,
		ttl:      ttl,
		lastSeen: make(map[string]time.Time),
	}
}

// Touch records activity for a hash (called on add and on each /play). Nil-safe
// and ignores empty hashes.
func (s *Sweeper) Touch(hash string) {
	if s == nil || hash == "" {
		return
	}
	s.mu.Lock()
	s.lastSeen[hash] = time.Now()
	s.mu.Unlock()
}

// Run sweeps on a ticker until ctx is cancelled.
func (s *Sweeper) Run(ctx context.Context) {
	interval := s.ttl / 2
	if interval < time.Minute {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweep(ctx, time.Now())
		}
	}
}

// sweep removes hashes idle beyond the TTL. Exposed logic kept small for testing.
func (s *Sweeper) sweep(ctx context.Context, now time.Time) {
	var stale []string
	s.mu.Lock()
	for hash, seen := range s.lastSeen {
		if now.Sub(seen) > s.ttl {
			stale = append(stale, hash)
			delete(s.lastSeen, hash)
		}
	}
	s.mu.Unlock()

	for _, hash := range stale {
		rmCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		if err := s.client.Remove(rmCtx, hash); err != nil {
			log.Printf("sweeper: remove %s: %v", hash, err)
		}
		cancel()
	}
}

// tracked returns how many hashes are currently tracked (test helper).
func (s *Sweeper) tracked() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.lastSeen)
}
