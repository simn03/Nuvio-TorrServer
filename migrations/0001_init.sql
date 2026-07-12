-- Users minted by the admin CLI. Token is the URL-path credential.
CREATE TABLE IF NOT EXISTS users (
  token       TEXT PRIMARY KEY,        -- 32 hex chars
  name        TEXT NOT NULL,
  created_at  INTEGER NOT NULL,        -- unix ms
  active      INTEGER NOT NULL DEFAULT 1
);

-- Option A: per-user config stored as a JSON blob keyed by token.
CREATE TABLE IF NOT EXISTS user_config (
  token       TEXT PRIMARY KEY REFERENCES users(token) ON DELETE CASCADE,
  config_json TEXT NOT NULL,
  updated_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS cinemeta_cache (
  imdb_id     TEXT NOT NULL,
  kind        TEXT NOT NULL,           -- movie | series
  payload     TEXT NOT NULL,           -- JSON: {name, year, ...}
  expires_at  INTEGER NOT NULL,
  PRIMARY KEY (imdb_id, kind)
);

CREATE TABLE IF NOT EXISTS prowlarr_cache (
  query_key   TEXT PRIMARY KEY,        -- hash of normalized query+categories
  payload     TEXT NOT NULL,           -- JSON: []Result
  expires_at  INTEGER NOT NULL
);
