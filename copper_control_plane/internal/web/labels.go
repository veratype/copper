package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"

	"copper_control_plane/internal/store"
)

type batchLabelsBody struct {
	IDs    []int64  `json:"ids"`
	Labels []string `json:"labels"`
	Mode   string   `json:"mode"` // add | remove (default add)
}

// POST /api/servers/labels — batch add or remove labels on selected servers.
func (s *Server) handleAPIBatchLabels(w http.ResponseWriter, r *http.Request) {
	var body batchLabelsBody
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(&body); err != nil {
		s.writeAPIJSON(w, http.StatusBadRequest, apiActionResult{OK: false, Error: "invalid JSON: " + err.Error()})
		return
	}
	labels, err := store.ParseLabelList(strings.Join(body.Labels, "\n"))
	if err != nil {
		s.writeAPIJSON(w, http.StatusBadRequest, apiActionResult{OK: false, Error: err.Error()})
		return
	}
	if len(body.IDs) == 0 {
		s.writeAPIJSON(w, http.StatusBadRequest, apiActionResult{OK: false, Error: "ids required"})
		return
	}
	if len(labels) == 0 {
		s.writeAPIJSON(w, http.StatusBadRequest, apiActionResult{OK: false, Error: "labels required"})
		return
	}
	mode := strings.ToLower(strings.TrimSpace(body.Mode))
	if mode == "" {
		mode = "add"
	}
	switch mode {
	case "add":
		err = s.store.AddLabelsToServers(body.IDs, labels)
	case "remove":
		err = s.store.RemoveLabelsFromServers(body.IDs, labels)
	default:
		s.writeAPIJSON(w, http.StatusBadRequest, apiActionResult{OK: false, Error: "mode must be add or remove"})
		return
	}
	if err != nil {
		s.writeAPIJSON(w, http.StatusInternalServerError, apiActionResult{OK: false, Error: err.Error()})
		return
	}
	s.writeAPIJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"mode":    mode,
		"ids":     body.IDs,
		"labels":  labels,
		"updated": len(body.IDs),
	})
}

func parseLabelsForm(r *http.Request) ([]string, error) {
	// Prefer multi-value "label" checkboxes / chips; fall back to free-text "labels".
	if vals := r.Form["label"]; len(vals) > 0 {
		return store.ParseLabelList(strings.Join(vals, "\n"))
	}
	return store.ParseLabelList(r.FormValue("labels"))
}

type serverListFilter struct {
	Labels []string
	Status string // "", declared, bound
	Q      string
}

func parseServerListFilter(q url.Values) serverListFilter {
	f := serverListFilter{
		Status: strings.ToLower(strings.TrimSpace(q.Get("status"))),
		Q:      strings.TrimSpace(q.Get("q")),
	}
	raw := q["label"]
	if len(raw) == 0 {
		if one := strings.TrimSpace(q.Get("labels")); one != "" {
			raw = []string{one}
		}
	}
	names, _ := store.ParseLabelList(strings.Join(raw, ","))
	f.Labels = names
	if f.Status != store.StatusDeclared && f.Status != store.StatusBound {
		f.Status = ""
	}
	return f
}

func (f serverListFilter) Active() bool {
	return len(f.Labels) > 0 || f.Status != "" || f.Q != ""
}

func applyServerListFilter(servers []store.Server, f serverListFilter) []store.Server {
	out := servers
	if len(f.Labels) > 0 {
		out = store.FilterServersByLabels(out, f.Labels)
	}
	if f.Status != "" {
		filtered := make([]store.Server, 0, len(out))
		for _, srv := range out {
			if srv.Status == f.Status {
				filtered = append(filtered, srv)
			}
		}
		out = filtered
	}
	if f.Q != "" {
		q := strings.ToLower(f.Q)
		filtered := make([]store.Server, 0, len(out))
		for _, srv := range out {
			if serverMatchesQuery(srv, q) {
				filtered = append(filtered, srv)
			}
		}
		out = filtered
	}
	return out
}

func serverMatchesQuery(srv store.Server, qLower string) bool {
	hay := []string{
		srv.Name, srv.Endpoint, srv.MAC, srv.ChassisID, srv.Username, srv.Notes,
	}
	hay = append(hay, srv.Labels...)
	for _, h := range hay {
		if strings.Contains(strings.ToLower(h), qLower) {
			return true
		}
	}
	return false
}
