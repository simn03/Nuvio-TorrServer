package store

import (
	"path/filepath"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestCinemetaCacheRoundTripAndExpiry(t *testing.T) {
	st := openTestStore(t)

	if _, ok := st.CinemetaCacheGet("tt1", "movie"); ok {
		t.Fatal("expected miss on empty cache")
	}

	if err := st.CinemetaCacheSet("tt1", "movie", []byte(`{"name":"X"}`), time.Hour); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, ok := st.CinemetaCacheGet("tt1", "movie")
	if !ok || string(got) != `{"name":"X"}` {
		t.Fatalf("get = %q, %v", got, ok)
	}

	// Distinct kind is a distinct key.
	if _, ok := st.CinemetaCacheGet("tt1", "series"); ok {
		t.Fatal("series should be a separate cache key")
	}

	// Already-expired entry reads as a miss (lazy-expire).
	if err := st.CinemetaCacheSet("tt2", "movie", []byte("stale"), -time.Second); err != nil {
		t.Fatalf("set expired: %v", err)
	}
	if _, ok := st.CinemetaCacheGet("tt2", "movie"); ok {
		t.Fatal("expired entry should miss")
	}
}

func TestProwlarrCacheAndPurge(t *testing.T) {
	st := openTestStore(t)

	_ = st.ProwlarrCacheSet("live", []byte("[]"), time.Hour)
	_ = st.ProwlarrCacheSet("dead", []byte("[]"), -time.Second)
	_ = st.CinemetaCacheSet("tt9", "movie", []byte("x"), -time.Second)

	n, err := st.PurgeExpiredCache()
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if n != 2 {
		t.Fatalf("purged %d rows, want 2 (dead prowlarr + expired cinemeta)", n)
	}
	if _, ok := st.ProwlarrCacheGet("live"); !ok {
		t.Fatal("live entry should survive purge")
	}
}
