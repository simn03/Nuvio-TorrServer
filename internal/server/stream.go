package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

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

// handleStream is a milestone-3 stub. The real resolver arrives in milestone
// 4/5; for now it reads the requesting user's live config and reflects it in
// the placeholder streams, proving that editing prefs changes stream output
// without a reinstall (the manifest URL never changes).
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	cfg, err := s.store.GetConfig(token)
	if err != nil {
		http.Error(w, "failed to load config", http.StatusInternalServerError)
		return
	}

	// Build MaxResults placeholder streams, one per enabled resolution (cycled),
	// titled to reflect the active config so changes are observable.
	streams := make([]stream, 0, cfg.MaxResults)
	for i := 0; i < cfg.MaxResults; i++ {
		res := cfg.Resolutions[i%len(cfg.Resolutions)]
		title := fmt.Sprintf("%s (stub #%d)\nsort=%s hevc=%t min_seed=%d",
			res, i+1, cfg.Sort, cfg.PreferHEVC, cfg.MinSeeders)
		if len(cfg.ExcludeQualities) > 0 {
			title += "\nexcluding: " + strings.Join(cfg.ExcludeQualities, ",")
		}
		streams = append(streams, stream{
			URL:   "https://example.invalid/stub",
			Name:  "Family TorrServer",
			Title: title,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(streamResponse{Streams: streams})
}
