package resolver

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/simn03/nuvio-p2p-http-addon/internal/cinemeta"
	"github.com/simn03/nuvio-p2p-http-addon/internal/config"
	"github.com/simn03/nuvio-p2p-http-addon/internal/prowlarr"
	"github.com/simn03/nuvio-p2p-http-addon/internal/torrserver"
)

// TestProfileLiveResolve drives the full Resolve pipeline against the *live*
// Prowlarr + TorrServer for a handful of real titles and prints a per-stage
// timing breakdown plus per-stage result attrition (raw -> filtered ->
// candidates -> streams). It exists to answer two questions from the field:
// (1) which stage is the largest wall-clock cost, and (2) where do results for
// titles like "Silo S01E01" / "Ascendance of a Bookworm S01E38" disappear.
//
// It is gated on PROFILE_LIVE so `go test ./...` stays green and offline. It
// must run somewhere the services resolve: inside the nuvio-torrserver
// container `prowlarr` (Docker DNS) and TorrServer (127.0.0.1:8090) are both
// reachable. Compile static, docker cp into the container, and exec it there.
//
// Env:
//
//	PROFILE_LIVE=1                (required; else the test skips)
//	PROWLARR_URL                  (default http://prowlarr:9696)
//	PROWLARR_API_KEY              (required for real results)
//	PROWLARR_INSECURE_TLS=true    (optional)
//	TORRSERVER_URL               (default http://127.0.0.1:8090)
//	CINEMETA_URL                 (default https://v3-cinemeta.strem.io)
//	PROFILE_CASES                (optional; "kind|id|label" entries joined by ';')
func TestProfileLiveResolve(t *testing.T) {
	if os.Getenv("PROFILE_LIVE") == "" {
		t.Skip("set PROFILE_LIVE=1 (and Prowlarr/TorrServer env) to run the live profiler")
	}

	prowURL := env("PROWLARR_URL", "http://prowlarr:9696")
	prowKey := os.Getenv("PROWLARR_API_KEY")
	torrURL := env("TORRSERVER_URL", "http://127.0.0.1:8090")
	cineURL := env("CINEMETA_URL", "https://v3-cinemeta.strem.io")
	insecure := os.Getenv("PROWLARR_INSECURE_TLS") == "true"

	cases := parseCases(os.Getenv("PROFILE_CASES"))

	// Real clients, cold cache (fakeCache always misses), preload off so it
	// doesn't skew the measured Resolve.
	cine := cinemeta.New(cineURL, fakeCache{}, time.Hour)
	prow := prowlarr.New(prowURL, prowKey, fakeCache{}, time.Hour, insecure)
	torr := torrserver.New(torrURL)
	sweeper := torrserver.NewSweeper(torr, 30*time.Minute)
	r := New(cine, prow, torr, sweeper, false)

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			cap := &capture{}
			prev := slog.Default()
			slog.SetDefault(slog.New(cap)) // Debug-level: capture EVERYTHING
			start := time.Now()
			streams, err := r.Resolve(context.Background(), tc.kind, tc.id, config.Default())
			total := time.Since(start)
			slog.SetDefault(prev)

			report(t, tc, streams, total, err, cap.records())
			if err == nil && len(streams) == 0 {
				t.Errorf("ZERO streams for %s (%s %s) — see attrition above", tc.label, tc.kind, tc.id)
			}
		})
	}
}

type profileCase struct{ kind, id, label string }

func defaultCases() []profileCase {
	return []profileCase{
		{"series", "tt14688458:1:1", "Silo S01E01"},
		{"series", "tt10885406:1:38", "Ascendance of a Bookworm S01E38"},
		{"movie", "tt1375666", "Inception (movie control)"},
	}
}

func parseCases(s string) []profileCase {
	if strings.TrimSpace(s) == "" {
		return defaultCases()
	}
	var out []profileCase
	for _, entry := range strings.Split(s, ";") {
		parts := strings.SplitN(strings.TrimSpace(entry), "|", 3)
		if len(parts) < 2 {
			continue
		}
		c := profileCase{kind: parts[0], id: parts[1], label: parts[1]}
		if len(parts) == 3 && parts[2] != "" {
			c.label = parts[2]
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return defaultCases()
	}
	return out
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// capture is a slog.Handler that records every log line (message + attrs) so we
// can reassemble the pipeline's own instrumentation after the run.
type capture struct {
	mu   sync.Mutex
	recs []logRec
}

type logRec struct {
	msg   string
	attrs map[string]any
}

func (c *capture) Enabled(context.Context, slog.Level) bool { return true }
func (c *capture) WithAttrs([]slog.Attr) slog.Handler       { return c }
func (c *capture) WithGroup(string) slog.Handler            { return c }
func (c *capture) Handle(_ context.Context, r slog.Record) error {
	m := make(map[string]any, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool { m[a.Key] = a.Value.Any(); return true })
	c.mu.Lock()
	c.recs = append(c.recs, logRec{msg: r.Message, attrs: m})
	c.mu.Unlock()
	return nil
}
func (c *capture) records() []logRec {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]logRec(nil), c.recs...)
}

func report(t *testing.T, tc profileCase, streams []Stream, total time.Duration, err error, recs []logRec) {
	var b strings.Builder
	fmt.Fprintf(&b, "\n=== %s  (%s %s) ===\n", tc.label, tc.kind, tc.id)
	if err != nil {
		fmt.Fprintf(&b, "ERROR: %v\n", err)
	}

	// Cinemeta.
	for _, r := range recs {
		if r.msg == "cinemeta fetch" || r.msg == "cinemeta cache hit" {
			fmt.Fprintf(&b, "cinemeta:   name=%q year=%v dur=%s\n",
				getStr(r, "name"), r.attrs["year"], getDur(r, "dur"))
		}
	}

	// Per-query search cost + result count.
	fmt.Fprintf(&b, "queries:\n")
	var maxQuery time.Duration
	var maxQueryName string
	for _, r := range recs {
		if r.msg == "prowlarr search" {
			d := getDur(r, "dur")
			fmt.Fprintf(&b, "  %-28q results=%-4d dur=%s\n", getStr(r, "query"), getInt(r, "results"), d)
			if d > maxQuery {
				maxQuery, maxQueryName = d, getStr(r, "query")
			}
		}
	}

	// Attrition + stage durations, straight from "candidates ready".
	for _, r := range recs {
		if r.msg == "resolve: candidates ready" {
			fmt.Fprintf(&b, "attrition:  raw=%d -> filtered=%d -> candidates=%d -> streams=%d\n",
				getInt(r, "raw_results"), getInt(r, "filtered_results"), getInt(r, "candidates"), len(streams))
			fmt.Fprintf(&b, "stages:     cinemeta=%s search=%s rank=%s (candidates_total=%s)\n",
				getDur(r, "cinemeta_dur"), getDur(r, "search_dur"), getDur(r, "rank_dur"), getDur(r, "total_dur"))
		}
	}

	// Per-candidate enrich cost — the suspected hotspot for season packs.
	fmt.Fprintf(&b, "enrich (per candidate):\n")
	var maxEnrich time.Duration
	var maxEnrichTitle string
	addByHash := map[string]time.Duration{}
	filesByHash := map[string]string{}
	for _, r := range recs {
		switch r.msg {
		case "torrserver add":
			addByHash[getStr(r, "hash")] = getDur(r, "dur")
		case "torrserver ensureFiles resolved", "torrserver ensureFiles gave up":
			filesByHash[getStr(r, "hash")] = fmt.Sprintf("ensureFiles(attempts=%d)=%s", getInt(r, "attempts"), getDur(r, "dur"))
		}
	}
	for _, r := range recs {
		switch r.msg {
		case "resolve: candidate registered":
			fmt.Fprintf(&b, "  registered %-55.55q file_index=%d dur=%s\n", getStr(r, "title"), getInt(r, "file_index"), getDur(r, "dur"))
		case "resolve: candidate added (no infohash)":
			fmt.Fprintf(&b, "  added      %-55.55q file_index=%d dur=%s\n", getStr(r, "title"), getInt(r, "file_index"), getDur(r, "dur"))
		case "resolve: candidate lazy registered":
			fmt.Fprintf(&b, "  lazy       %-55.55q dur=%s\n", getStr(r, "title"), getDur(r, "dur"))
		case "resolve: candidate enriched":
			d := getDur(r, "dur")
			detail := ""
			if h := getStr(r, "hash"); h != "" {
				if ad, ok := addByHash[h]; ok {
					detail += " add=" + ad.String()
				}
				if fd, ok := filesByHash[h]; ok {
					detail += " " + fd
				}
			}
			fmt.Fprintf(&b, "  add+files  %-55.55q dur=%s%s\n", getStr(r, "title"), d, detail)
			if d > maxEnrich {
				maxEnrich, maxEnrichTitle = d, getStr(r, "title")
			}
		case "resolve: candidate dropped":
			fmt.Fprintf(&b, "  DROPPED    %-55.55q err=%v\n", getStr(r, "title"), r.attrs["err"])
		}
	}

	fmt.Fprintf(&b, "TOTAL Resolve: %s  (streams=%d)\n", total, len(streams))

	// Name the single largest contributor we can see.
	switch {
	case maxEnrich >= maxQuery && maxEnrich > 0:
		fmt.Fprintf(&b, "LARGEST STAGE: enrich %q (%s)\n", trunc(maxEnrichTitle, 40), maxEnrich)
	case maxQuery > 0:
		fmt.Fprintf(&b, "LARGEST STAGE: search %q (%s)\n", maxQueryName, maxQuery)
	}
	t.Log(b.String())
}

func getStr(r logRec, key string) string {
	if v, ok := r.attrs[key].(string); ok {
		return v
	}
	return ""
}

func getDur(r logRec, key string) time.Duration {
	if v, ok := r.attrs[key].(time.Duration); ok {
		return v
	}
	return 0
}

func getInt(r logRec, key string) int {
	switch v := r.attrs[key].(type) {
	case int64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
