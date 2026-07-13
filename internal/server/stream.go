package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"nuvio-torrserver/internal/rank"

	"github.com/go-chi/chi/v5"
)

// streamResponse is the Stremio stream resource payload.
type streamResponse struct {
	Streams []stream `json:"streams"`
}

type stream struct {
	URL           string               `json:"url"`
	Name          string               `json:"name,omitempty"`
	Title         string               `json:"title,omitempty"`
	BehaviorHints *streamBehaviorHints `json:"behaviorHints,omitempty"`
}

type streamBehaviorHints struct {
	BingeGroup string `json:"bingeGroup,omitempty"`
}

// handleStream runs the resolver for the requesting user's live config and
// returns a ranked stream list. In milestone 4 the URL is the raw source link;
// milestone 5 replaces it with a signed /play URL backed by TorrServer.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	typ := chi.URLParam(r, "type")
	id := chi.URLParam(r, "id")

	cfg, err := s.store.GetConfig(token)
	if err != nil {
		http.Error(w, "failed to load config", http.StatusInternalServerError)
		return
	}

	resolved, err := s.resolver.Resolve(r.Context(), typ, id, cfg)
	if err != nil {
		// Return an empty (valid) list rather than an error so Stremio shows
		// "no streams" instead of failing the addon.
		writeStreams(w, nil)
		return
	}

	streams := make([]stream, 0, len(resolved))
	for _, rs := range resolved {
		streams = append(streams, stream{
			URL:   s.buildPlayURL(r, rs.Hash, rs.FileIndex),
			Name:  streamName(rs.Candidate),
			Title: streamTitle(rs.Candidate),
			BehaviorHints: &streamBehaviorHints{
				BingeGroup: "nuvio-torrserver-" + rs.Candidate.Parsed.Resolution,
			},
		})
	}
	writeStreams(w, streams)
}

func writeStreams(w http.ResponseWriter, streams []stream) {
	if streams == nil {
		streams = []stream{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(streamResponse{Streams: streams})
}

// streamName is the short left-column label: the addon plus the resolution.
func streamName(c rank.Candidate) string {
	if res := c.Parsed.Resolution; res != "" && res != "unknown" {
		return "Nuvio\n" + res
	}
	return "Nuvio"
}

// streamTitle builds the detail shown for each result: the full release title,
// a badge line, and the source indexer (§10, plus the per-user request to show
// the full torrent name and where it came from).
func streamTitle(c rank.Candidate) string {
	lines := []string{c.Result.Title}

	var badges []string
	if c.Parsed.Resolution != "" && c.Parsed.Resolution != "unknown" {
		badges = append(badges, c.Parsed.Resolution)
	}
	if c.Parsed.Quality != "" {
		badges = append(badges, c.Parsed.Quality)
	}
	if c.Parsed.IsHEVC {
		badges = append(badges, "HEVC")
	}
	meta := fmt.Sprintf("👤 %d · 💾 %s", c.Result.Seeders, humanSize(c.Result.Size))
	if len(badges) > 0 {
		meta = strings.Join(badges, " ") + " · " + meta
	}
	lines = append(lines, meta)

	if c.Result.Indexer != "" {
		lines = append(lines, "🔎 "+c.Result.Indexer)
	}
	return strings.Join(lines, "\n")
}

func humanSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
