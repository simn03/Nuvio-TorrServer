package server

import (
	"encoding/json"
	"net/http"
)

// manifest is the Stremio addon manifest (§10).
type manifest struct {
	ID            string        `json:"id"`
	Version       string        `json:"version"`
	Name          string        `json:"name"`
	Description   string        `json:"description"`
	Resources     []string      `json:"resources"`
	Types         []string      `json:"types"`
	IDPrefixes    []string      `json:"idPrefixes"`
	BehaviorHints behaviorHints `json:"behaviorHints"`
}

type behaviorHints struct {
	Configurable          bool `json:"configurable"`
	ConfigurationRequired bool `json:"configurationRequired"`
}

const addonVersion = "0.1.0"

func (s *Server) handleManifest(w http.ResponseWriter, _ *http.Request) {
	m := manifest{
		ID:          "community.family.torrserver",
		Version:     addonVersion,
		Name:        "Nuvio TorrServer",
		Description: "Self-hosted P2P streaming for multiple members via a private TorrServer.",
		Resources:   []string{"stream"},
		Types:       []string{"movie", "series"},
		IDPrefixes:  []string{"tt"},
		BehaviorHints: behaviorHints{
			Configurable:          true,
			ConfigurationRequired: false,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(m)
}
