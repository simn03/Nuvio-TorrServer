package server

import (
	"context"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/simn03/nuvio-p2p-http-addon/internal/config"

	"github.com/go-chi/chi/v5"
)

// configPage is the view model for the configure template.
type configPage struct {
	Saved  bool
	Config config.UserConfig
	Sorts  []string
	// Resolutions/Excludable are rendered as checkbox rows with a checked flag.
	Resolutions []checkboxItem
	Excludable  []checkboxItem
	// Indexers is the per-user indexer selection; IndexersAvailable is false when
	// Prowlarr couldn't be reached to list them.
	Indexers          []indexerItem
	IndexersAvailable bool
}

type checkboxItem struct {
	Value   string
	Checked bool
}

type indexerItem struct {
	ID      int
	Name    string
	Checked bool
}

var configTmpl = template.Must(template.New("configure").Parse(configHTML))

// handleConfigureGet renders the form bound to the user's current config.
func (s *Server) handleConfigureGet(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	cfg, err := s.store.GetConfig(token)
	if err != nil {
		http.Error(w, "failed to load config", http.StatusInternalServerError)
		return
	}
	s.renderConfig(r.Context(), w, cfg, false)
}

// handleConfigurePost validates the submitted form, persists it, and re-renders
// with a saved confirmation. No new URL, no reinstall (§10).
func (s *Server) handleConfigurePost(w http.ResponseWriter, r *http.Request) {
	token := chi.URLParam(r, "token")
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	cfg := config.UserConfig{
		Sort:             r.FormValue("sort"),
		Resolutions:      r.Form["resolutions"],
		ExcludeQualities: r.Form["exclude"],
		PreferHEVC:       r.FormValue("preferHEVC") != "",
		MaxSizeGB:        parseFloat(r.FormValue("maxSizeGB"), 0),
		MinSeeders:       parseInt(r.FormValue("minSeeders"), 3),
		MaxResults:       parseInt(r.FormValue("maxResults"), 5),
		Indexers:         parseInts(r.Form["indexers"]),
	}
	// The indexer fieldset only renders when Prowlarr is reachable. If it wasn't
	// (marker absent), preserve the existing selection instead of wiping it.
	if r.FormValue("indexers_present") != "1" {
		if prev, err := s.store.GetConfig(token); err == nil {
			cfg.Indexers = prev.Indexers
		}
	}
	// Normalize (also done in SetConfig) so the re-render reflects the stored form.
	cfg = config.Normalize(cfg)

	if err := s.store.SetConfig(token, cfg); err != nil {
		slog.ErrorContext(r.Context(), "configure: save failed", "err", err)
		http.Error(w, "failed to save config", http.StatusInternalServerError)
		return
	}
	slog.InfoContext(r.Context(), "configure: saved", "sort", cfg.Sort, "min_seeders", cfg.MinSeeders, "max_results", cfg.MaxResults)
	s.renderConfig(r.Context(), w, cfg, true)
}

func (s *Server) renderConfig(ctx context.Context, w http.ResponseWriter, cfg config.UserConfig, saved bool) {
	indexers, available := s.indexerItems(ctx, cfg.Indexers)
	page := configPage{
		Saved:             saved,
		Config:            cfg,
		Sorts:             config.AllSorts,
		Resolutions:       checkboxes(config.AllResolutions, cfg.Resolutions),
		Excludable:        checkboxes(config.AllExcludable, cfg.ExcludeQualities),
		Indexers:          indexers,
		IndexersAvailable: available,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := configTmpl.Execute(w, page); err != nil {
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

// indexerItems fetches the enabled torrent indexers from Prowlarr and marks
// which are selected. An empty selection means "all", so every box shows checked.
func (s *Server) indexerItems(ctx context.Context, selected []int) ([]indexerItem, bool) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	list, err := s.prow.Indexers(ctx)
	if err != nil {
		slog.WarnContext(ctx, "configure: indexer list unavailable", "err", err)
		return nil, false
	}
	sel := make(map[int]bool, len(selected))
	for _, id := range selected {
		sel[id] = true
	}
	all := len(selected) == 0
	var items []indexerItem
	for _, ix := range list {
		if !ix.Enable || ix.Protocol != "torrent" {
			continue
		}
		items = append(items, indexerItem{ID: ix.ID, Name: ix.Name, Checked: all || sel[ix.ID]})
	}
	return items, true
}

func checkboxes(all, selected []string) []checkboxItem {
	sel := make(map[string]bool, len(selected))
	for _, s := range selected {
		sel[s] = true
	}
	items := make([]checkboxItem, len(all))
	for i, v := range all {
		items[i] = checkboxItem{Value: v, Checked: sel[v]}
	}
	return items
}

func parseInt(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

func parseInts(ss []string) []int {
	var out []int
	for _, s := range ss {
		if n, err := strconv.Atoi(s); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func parseFloat(s string, def float64) float64 {
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return def
}

const configHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Nuvio TorrServer — Configure</title>
<style>
  :root { color-scheme: light dark; }
  body { font-family: system-ui, sans-serif; max-width: 620px; margin: 2rem auto; padding: 0 1rem; line-height: 1.5; }
  h1 { font-size: 1.4rem; }
  fieldset { border: 1px solid #8886; border-radius: 8px; margin: 1rem 0; padding: 0.75rem 1rem; }
  legend { font-weight: 600; padding: 0 0.4rem; }
  label { display: inline-block; margin-right: 1rem; }
  .row { margin: 0.5rem 0; }
  .cols { columns: 2; }
  .cols label { display: block; }
  input[type=number] { width: 6rem; }
  button { font-size: 1rem; padding: 0.5rem 1.2rem; border-radius: 8px; cursor: pointer; }
  .saved { background: #1a7f37; color: #fff; padding: 0.6rem 1rem; border-radius: 8px; margin-bottom: 1rem; }
  .hint { color: #8888; font-size: 0.85rem; }
</style>
</head>
<body>
<h1>Configure your streams</h1>
{{if .Saved}}<div class="saved">✓ Saved. Changes apply immediately — no reinstall needed.</div>{{end}}
<form method="post">
  <fieldset>
    <legend>Sort by</legend>
    {{range .Sorts}}
      <label><input type="radio" name="sort" value="{{.}}" {{if eq . $.Config.Sort}}checked{{end}}> {{.}}</label>
    {{end}}
  </fieldset>

  <fieldset>
    <legend>Resolutions</legend>
    {{range .Resolutions}}
      <label><input type="checkbox" name="resolutions" value="{{.Value}}" {{if .Checked}}checked{{end}}> {{.Value}}</label>
    {{end}}
    <div class="hint">Unchecking all is treated as "all enabled".</div>
  </fieldset>

  <fieldset>
    <legend>Exclude release types</legend>
    {{range .Excludable}}
      <label><input type="checkbox" name="exclude" value="{{.Value}}" {{if .Checked}}checked{{end}}> {{.Value}}</label>
    {{end}}
  </fieldset>

  <fieldset>
    <legend>Indexers</legend>
    {{if .IndexersAvailable}}
      <input type="hidden" name="indexers_present" value="1">
      <div class="cols">
      {{range .Indexers}}
        <label><input type="checkbox" name="indexers" value="{{.ID}}" {{if .Checked}}checked{{end}}> {{.Name}}</label>
      {{end}}
      </div>
      <div class="hint">Only the selected indexers are searched. Fewer, faster indexers = quicker results. Unchecking all is treated as "all".</div>
    {{else}}
      <div class="hint">Couldn't reach Prowlarr to list indexers right now — leaving this unchanged searches all of them.</div>
    {{end}}
  </fieldset>

  <fieldset>
    <legend>Other</legend>
    <div class="row">
      <label><input type="checkbox" name="preferHEVC" {{if .Config.PreferHEVC}}checked{{end}}> Prefer HEVC/x265</label>
    </div>
    <div class="row">
      <label>Max size (GB, 0 = no cap):
        <input type="number" name="maxSizeGB" min="0" step="0.1" value="{{.Config.MaxSizeGB}}"></label>
    </div>
    <div class="row">
      <label>Min seeders:
        <input type="number" name="minSeeders" min="0" step="1" value="{{.Config.MinSeeders}}"></label>
    </div>
    <div class="row">
      <label>Max results (1–50):
        <input type="number" name="maxResults" min="1" max="50" step="1" value="{{.Config.MaxResults}}"></label>
    </div>
  </fieldset>

  <button type="submit">Save</button>
</form>
</body>
</html>`
