package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/simn03/nuvio-p2p-http-addon/internal/config"
)

// GetConfig returns the user's stored config, or the defaults if no row exists
// yet (§6 store contract).
func (s *Store) GetConfig(token string) (config.UserConfig, error) {
	var blob string
	err := s.db.QueryRow(`SELECT config_json FROM user_config WHERE token = ?`, token).Scan(&blob)
	if errors.Is(err, sql.ErrNoRows) {
		return config.Default(), nil
	}
	if err != nil {
		return config.Default(), fmt.Errorf("get config: %w", err)
	}
	c, err := config.Unmarshal([]byte(blob))
	if err != nil {
		// Corrupt/legacy blob: fall back to defaults rather than failing the request.
		return config.Default(), nil
	}
	return c, nil
}

// SetConfig upserts the user's config (normalising before persisting).
func (s *Store) SetConfig(token string, c config.UserConfig) error {
	c = config.Normalize(c)
	blob, err := config.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	_, err = s.db.Exec(
		`INSERT INTO user_config (token, config_json, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(token) DO UPDATE SET config_json = excluded.config_json, updated_at = excluded.updated_at`,
		token, string(blob), time.Now().UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("set config: %w", err)
	}
	return nil
}
