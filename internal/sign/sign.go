// Package sign creates and verifies short-lived HMAC signatures for /play URLs
// (§11). The payload binds a torrent hash, file index, and expiry so a leaked
// URL dies within PLAY_URL_TTL and TorrServer is never directly reachable.
package sign

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
)

// Signing errors, mapped by the handler to 403 (bad sig) / 410 (expired).
var (
	ErrBadSig  = errors.New("invalid signature")
	ErrExpired = errors.New("signature expired")
)

// Signer holds the HMAC secret.
type Signer struct {
	secret []byte
}

// New builds a Signer.
func New(secret string) *Signer {
	return &Signer{secret: []byte(secret)}
}

// payload is the canonical signed string "{hash}:{index}:{season}:{episode}:{exp}".
// season/episode are 0 for movies and exact-episode releases; they are non-zero
// only for deferred season packs, where /play needs them to pick the file.
func payload(hash string, index, season, episode int, exp int64) string {
	return fmt.Sprintf("%s:%d:%d:%d:%d", hash, index, season, episode, exp)
}

// Sign returns the hex HMAC-SHA256 of the payload.
func (s *Signer) Sign(hash string, index, season, episode int, exp int64) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(payload(hash, index, season, episode, exp)))
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify checks the signature (constant time) then the expiry. nowMs is the
// current unix-ms time. A bad signature returns ErrBadSig; a valid-but-expired
// signature returns ErrExpired.
func (s *Signer) Verify(hash string, index, season, episode int, exp int64, sig string, nowMs int64) error {
	want := s.Sign(hash, index, season, episode, exp)
	got, err := hex.DecodeString(sig)
	if err != nil {
		return ErrBadSig
	}
	wantBytes, _ := hex.DecodeString(want)
	if subtle.ConstantTimeCompare(got, wantBytes) != 1 {
		return ErrBadSig
	}
	if exp <= nowMs {
		return ErrExpired
	}
	return nil
}

// PlayPath builds the signed path "/play/{hash}/{index}?exp=&sig=", appending
// "&s=&e=" only for deferred season packs (season/episode non-zero). The host
// is prefixed by the caller.
func (s *Signer) PlayPath(hash string, index, season, episode int, exp int64) string {
	sig := s.Sign(hash, index, season, episode, exp)
	if season == 0 && episode == 0 {
		return fmt.Sprintf("/play/%s/%d?exp=%d&sig=%s", hash, index, exp, sig)
	}
	return fmt.Sprintf("/play/%s/%d?exp=%d&sig=%s&s=%d&e=%d", hash, index, exp, sig, season, episode)
}
