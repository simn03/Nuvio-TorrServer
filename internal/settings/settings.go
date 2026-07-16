// Package settings loads process configuration from the environment (§4 of the
// build plan). This is distinct from per-user UserConfig, which lives in the DB.
package settings

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Settings is the fully-resolved process configuration.
type Settings struct {
	Port                  string
	PublicHost            string // may be empty; server falls back to request Host
	TorrServerURL         string
	ProwlarrURL           string
	ProwlarrAPIKey        string
	ProwlarrInsecureTLS   bool
	CinemetaURL           string
	DBPath                string
	SigningSecret         string
	PlayURLTTL            time.Duration
	ProwlarrCacheTTL      time.Duration
	ProwlarrSearchTimeout time.Duration
	CinemetaCacheTTL      time.Duration
	TorrServerPreload     bool
	TorrentIdleTTL        time.Duration
	LogLevel              string // "debug" | "info" | "warn" | "error"
	LogFormat             string // "text" | "json"
}

// Load reads settings from the environment, applying defaults from §4 and
// failing fast when a required value is missing or left at a placeholder.
func Load() (*Settings, error) {
	s := &Settings{
		Port:                getenv("PORT", "7000"),
		PublicHost:          os.Getenv("PUBLIC_HOST"),
		TorrServerURL:       getenv("TORRSERVER_URL", "http://127.0.0.1:8090"),
		ProwlarrURL:         os.Getenv("PROWLARR_URL"),
		ProwlarrAPIKey:      os.Getenv("PROWLARR_API_KEY"),
		ProwlarrInsecureTLS: getbool("PROWLARR_INSECURE_TLS", false),
		CinemetaURL:         getenv("CINEMETA_URL", "https://v3-cinemeta.strem.io"),
		DBPath:              getenv("DB_PATH", "/data/addon/addon.db"),
		SigningSecret:       os.Getenv("SIGNING_SECRET"),
		TorrServerPreload:   getbool("TORRSERVER_PRELOAD", true),
		LogLevel:            getenv("LOG_LEVEL", "info"),
		LogFormat:           getenv("LOG_FORMAT", "text"),
	}

	var err error
	if s.PlayURLTTL, err = getdur("PLAY_URL_TTL", 6*time.Hour); err != nil {
		return nil, err
	}
	if s.ProwlarrCacheTTL, err = getdur("PROWLARR_CACHE_TTL", 12*time.Hour); err != nil {
		return nil, err
	}
	// Per-query search deadline: a slow indexer past this is cut so it can't
	// hold the whole stream response. Results it would have returned surface on
	// a later fetch via the accumulating Prowlarr cache.
	if s.ProwlarrSearchTimeout, err = getdur("PROWLARR_SEARCH_TIMEOUT", 12*time.Second); err != nil {
		return nil, err
	}
	if s.CinemetaCacheTTL, err = getdur("CINEMETA_CACHE_TTL", 720*time.Hour); err != nil {
		return nil, err
	}
	if s.TorrentIdleTTL, err = getdur("TORRENT_IDLE_TTL", 30*time.Minute); err != nil {
		return nil, err
	}

	// Fail-fast on required secrets/services (§4, milestone 1).
	var missing []string
	if strings.TrimSpace(s.SigningSecret) == "" || isPlaceholder(s.SigningSecret) {
		missing = append(missing, "SIGNING_SECRET (must be set to a strong, non-default value)")
	}
	if strings.TrimSpace(s.ProwlarrURL) == "" {
		missing = append(missing, "PROWLARR_URL")
	}
	if strings.TrimSpace(s.ProwlarrAPIKey) == "" {
		missing = append(missing, "PROWLARR_API_KEY")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required environment: %s", strings.Join(missing, ", "))
	}

	return s, nil
}

// AdminSettings is the minimal config the CLI subcommands need. Admin ops run
// via `docker exec` and must not require the serve-only secrets (Prowlarr /
// signing), so they use this loader instead of Load.
type AdminSettings struct {
	DBPath     string
	PublicHost string
}

// LoadAdmin reads only the settings the admin CLI needs.
func LoadAdmin() *AdminSettings {
	return &AdminSettings{
		DBPath:     getenv("DB_PATH", "/data/addon/addon.db"),
		PublicHost: os.Getenv("PUBLIC_HOST"),
	}
}

func isPlaceholder(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "changeme", "change-me", "default", "secret", "your-secret-here":
		return true
	}
	return false
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getbool(key string, def bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func getdur(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid duration for %s: %q", key, v)
	}
	return d, nil
}
