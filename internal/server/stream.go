package server

import (
	"encoding/json"
	"net/http"
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

// handleStream is a milestone-2 stub: it proves the authed route works and
// returns an empty (but valid) stream list. The real resolver arrives in
// milestone 4/5.
func (s *Server) handleStream(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(streamResponse{Streams: []stream{}})
}
