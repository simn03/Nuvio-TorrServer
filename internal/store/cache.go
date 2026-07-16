package store

import "time"

// The cache uses the actual schema from §6: cinemeta_cache is keyed by
// (imdb_id, kind); prowlarr_cache by a single query_key. Reads lazy-expire
// (an entry past expires_at reads as a miss); PurgeExpiredCache reclaims rows.

// CinemetaCacheGet returns the cached payload for (imdb, kind) if present and
// unexpired.
func (s *Store) CinemetaCacheGet(imdb, kind string) ([]byte, bool) {
	var (
		payload   string
		expiresAt int64
	)
	err := s.db.QueryRow(
		`SELECT payload, expires_at FROM cinemeta_cache WHERE imdb_id = ? AND kind = ?`,
		imdb, kind,
	).Scan(&payload, &expiresAt)
	if err != nil || expiresAt <= nowMs() {
		return nil, false
	}
	return []byte(payload), true
}

// CinemetaCacheSet upserts a cinemeta cache entry with the given TTL.
func (s *Store) CinemetaCacheSet(imdb, kind string, payload []byte, ttl time.Duration) error {
	_, err := s.db.Exec(
		`INSERT INTO cinemeta_cache (imdb_id, kind, payload, expires_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(imdb_id, kind) DO UPDATE SET payload = excluded.payload, expires_at = excluded.expires_at`,
		imdb, kind, string(payload), nowMs()+ttl.Milliseconds(),
	)
	return err
}

// ProwlarrCacheGet returns the cached payload for key if present and unexpired.
func (s *Store) ProwlarrCacheGet(key string) ([]byte, bool) {
	var (
		payload   string
		expiresAt int64
	)
	err := s.db.QueryRow(
		`SELECT payload, expires_at FROM prowlarr_cache WHERE query_key = ?`, key,
	).Scan(&payload, &expiresAt)
	if err != nil || expiresAt <= nowMs() {
		return nil, false
	}
	return []byte(payload), true
}

// ProwlarrCacheSet upserts a prowlarr cache entry with the given TTL.
func (s *Store) ProwlarrCacheSet(key string, payload []byte, ttl time.Duration) error {
	_, err := s.db.Exec(
		`INSERT INTO prowlarr_cache (query_key, payload, expires_at) VALUES (?, ?, ?)
		 ON CONFLICT(query_key) DO UPDATE SET payload = excluded.payload, expires_at = excluded.expires_at`,
		key, string(payload), nowMs()+ttl.Milliseconds(),
	)
	return err
}

// EnrichCacheGet returns the cached file index for key if present and unexpired.
func (s *Store) EnrichCacheGet(key string) (int, bool) {
	var (
		index     int
		expiresAt int64
	)
	err := s.db.QueryRow(
		`SELECT file_index, expires_at FROM enrich_cache WHERE cache_key = ?`, key,
	).Scan(&index, &expiresAt)
	if err != nil || expiresAt <= nowMs() {
		return 0, false
	}
	return index, true
}

// EnrichCacheSet upserts a play-time file-selection entry with the given TTL.
func (s *Store) EnrichCacheSet(key string, index int, ttl time.Duration) error {
	_, err := s.db.Exec(
		`INSERT INTO enrich_cache (cache_key, file_index, expires_at) VALUES (?, ?, ?)
		 ON CONFLICT(cache_key) DO UPDATE SET file_index = excluded.file_index, expires_at = excluded.expires_at`,
		key, index, nowMs()+ttl.Milliseconds(),
	)
	return err
}

// PurgeExpiredCache deletes expired rows from the cache tables and returns the
// number removed. Intended to run periodically.
func (s *Store) PurgeExpiredCache() (int64, error) {
	now := nowMs()
	var total int64
	for _, q := range []string{
		`DELETE FROM cinemeta_cache WHERE expires_at <= ?`,
		`DELETE FROM prowlarr_cache WHERE expires_at <= ?`,
		`DELETE FROM enrich_cache WHERE expires_at <= ?`,
	} {
		res, err := s.db.Exec(q, now)
		if err != nil {
			return total, err
		}
		if n, err := res.RowsAffected(); err == nil {
			total += n
		}
	}
	return total, nil
}

func nowMs() int64 { return time.Now().UnixMilli() }
