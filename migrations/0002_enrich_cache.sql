-- Memoizes play-time file selection for deferred season packs: maps a
-- (hash, season, episode) key to the chosen TorrServer file index so seeks and
-- replays don't re-poll TorrServer's file list.
CREATE TABLE IF NOT EXISTS enrich_cache (
  cache_key   TEXT PRIMARY KEY,       -- hash|season|episode
  file_index  INTEGER NOT NULL,
  expires_at  INTEGER NOT NULL
);
