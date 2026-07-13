package store

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/simn03/nuvio-p2p-http-addon/internal/config"
)

// ErrNotFound is returned when a token has no matching user row.
var ErrNotFound = errors.New("user not found")

// User is a minted member.
type User struct {
	Token     string
	Name      string
	CreatedAt time.Time
	Active    bool
}

// newToken returns a 32-hex-char (16-byte) random token.
func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// CreateUser mints a new active user and returns its token.
func (s *Store) CreateUser(name string) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec(
		`INSERT INTO users (token, name, created_at, active) VALUES (?, ?, ?, 1)`,
		token, name, time.Now().UnixMilli(),
	)
	if err != nil {
		return "", fmt.Errorf("insert user: %w", err)
	}
	// Seed default config (§7). GetConfig also falls back to defaults, so this
	// is a convenience so the row exists and is editable immediately.
	if err := s.SetConfig(token, config.Default()); err != nil {
		return "", fmt.Errorf("seed config: %w", err)
	}
	return token, nil
}

// IsValid reports whether the token exists and is active.
func (s *Store) IsValid(token string) bool {
	var active int
	err := s.db.QueryRow(`SELECT active FROM users WHERE token = ?`, token).Scan(&active)
	if err != nil {
		return false
	}
	return active == 1
}

// Revoke deactivates a token. Returns ErrNotFound if no such token exists.
func (s *Store) Revoke(token string) error {
	res, err := s.db.Exec(`UPDATE users SET active = 0 WHERE token = ?`, token)
	if err != nil {
		return fmt.Errorf("revoke: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("revoke rows: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ListUsers returns all users ordered by creation time (oldest first).
func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.db.Query(`SELECT token, name, created_at, active FROM users ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	var users []User
	for rows.Next() {
		var (
			u         User
			createdMs int64
			active    int
		)
		if err := rows.Scan(&u.Token, &u.Name, &createdMs, &active); err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		u.CreatedAt = time.UnixMilli(createdMs)
		u.Active = active == 1
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate users: %w", err)
	}
	return users, nil
}
