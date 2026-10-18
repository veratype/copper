package web

import (
	"net/http"
	"strconv"

	"copper_control_plane/internal/store"
)

func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	rt, err := s.store.GetBatchRuntime()
	if err != nil {
		s.render(w, "settings.html", pageData{
			Title: "Settings", Authed: true, Error: err.Error(),
			BatchRuntime: store.DefaultBatchRuntime(),
		})
		return
	}
	data := pageData{
		Title:        "Settings",
		Authed:       true,
		BatchRuntime: rt,
	}
	if flash := r.URL.Query().Get("flash"); flash != "" {
		data.Flash = flash
	}
	s.render(w, "settings.html", data)
}

func (s *Server) handleSettingsPost(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	rt := store.DefaultBatchRuntime()
	if v := r.FormValue("executors"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			rt.Executors = n
		}
	}
	if v := r.FormValue("timeout_inventory"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			rt.Timeouts.Inventory = n
		}
	}
	if v := r.FormValue("timeout_reboot"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			rt.Timeouts.Reboot = n
		}
	}
	if v := r.FormValue("timeout_mount"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			rt.Timeouts.Mount = n
		}
	}
	rt = rt.Normalize()
	if err := s.store.SaveBatchRuntime(rt); err != nil {
		s.render(w, "settings.html", pageData{
			Title: "Settings", Authed: true, Error: err.Error(), BatchRuntime: rt,
		})
		return
	}
	http.Redirect(w, r, "/settings?flash="+urlQuery("Settings saved"), http.StatusSeeOther)
}

func (s *Server) handleAPIBatchSettings(w http.ResponseWriter, _ *http.Request) {
	rt, err := s.store.GetBatchRuntime()
	if err != nil {
		s.writeAPIJSON(w, http.StatusInternalServerError, map[string]any{
			"ok": false, "error": err.Error(),
		})
		return
	}
	s.writeAPIJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"settings": rt,
		"limits": map[string]any{
			"max_executors": store.MaxBatchExecutors,
		},
	})
}
