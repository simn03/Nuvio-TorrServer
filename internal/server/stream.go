package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"family-torrserver/internal/rank"

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

	candidates, err := s.resolver.Resolve(r.Context(), typ, id, cfg)
	if err != nil {
		// Return an empty (valid) list rather than an error so Stremio shows
		// "no streams" instead of failing the addon.
		writeStreams(w, nil)
		return
	}

	streams := make([]stream, 0, len(candidates))
	for _, c := range candidates {
		streams = append(streams, stream{
			URL:   c.Result.Link(),
			Name:  "Family TorrServer",
			Title: streamTitle(c),
			BehaviorHints: &streamBehaviorHints{
				BingeGroup: "family-torrserver-" + c.Parsed.Resolution,
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

// streamTitle builds a Torrentio-style, human-scannable title (§10).
func streamTitle(c rank.Candidate) string {
	var parts []string
	if c.Parsed.Resolution != "" && c.Parsed.Resolution != "unknown" {
		parts = append(parts, c.Parsed.Resolution)
	}
	if c.Parsed.Quality != "" {
		parts = append(parts, c.Parsed.Quality)
	}
	if c.Parsed.IsHEVC {
		parts = append(parts, "HEVC")
	}
	line1 := strings.Join(parts, " ")
	if line1 == "" {
		line1 = c.Result.Title
	}
	line2 := fmt.Sprintf("👤 %d  💾 %s", c.Result.Seeders, humanSize(c.Result.Size))
	return line1 + "\n" + line2
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
