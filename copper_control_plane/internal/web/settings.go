package web

import (
	"net/http"
	"strconv"

	"copper_control_plane/internal/datamedia"
	"copper_control_plane/internal/store"
)

func (s *Server) settingsPageData(rt store.BatchRuntime, live store.LiveOSSSH, dm store.DataMedia, sess store.SessionSettings, errMsg string) pageData {
	return pageData{
		Title:        "Settings",
		Authed:       true,
		Error:        errMsg,
		BatchRuntime: rt,
		LiveOSSSH:    live,
		DataMedia:    dm,
		Session:      sess,
	}
}

func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	rt, err := s.store.GetBatchRuntime()
	if err != nil {
		s.render(w, "settings.html", s.settingsPageData(
			store.DefaultBatchRuntime(), store.DefaultLiveOSSSH(), store.DefaultDataMedia(),
			store.DefaultSessionSettings(), err.Error(),
		))
		return
	}
	live, _ := s.store.GetLiveOSSSH()
	dm, _ := s.store.GetDataMedia()
	sess, _ := s.store.GetSessionSettings()
	data := s.settingsPageData(rt, live, dm, sess, "")
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
	if v := r.FormValue("agent_watch_seconds"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			rt.AgentWatchSeconds = n
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
	if v := r.FormValue("timeout_probe"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			rt.Timeouts.Probe = n
		}
	}
	if v := r.FormValue("timeout_delete"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			rt.Timeouts.Delete = n
		}
	}
	if v := r.FormValue("timeout_detect"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			rt.Timeouts.Detect = n
		}
	}
	if v := r.FormValue("timeout_provision"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			rt.Timeouts.Provision = n
		}
	}
	rt = rt.Normalize()

	sess := store.DefaultSessionSettings()
	if v := r.FormValue("session_timeout_hours"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			sess.TimeoutHours = n
		}
	}
	sess = sess.Normalize()

	live := store.LiveOSSSH{
		Username: r.FormValue("live_os_username"),
		Password: r.FormValue("live_os_password"),
	}
	if v := r.FormValue("live_os_port"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			live.Port = n
		}
	}
	// Keep previous password if the form left it blank (browser password managers / intentional leave-as-is).
	if live.Password == "" {
		prev, _ := s.store.GetLiveOSSSH()
		live.Password = prev.Password
	}
	live = live.Normalize()

	dm := store.DataMedia{
		Enabled: r.FormValue("data_media_enabled") == "on" || r.FormValue("data_media_enabled") == "1" || r.FormValue("data_media_enabled") == "true",
		Folder:  r.FormValue("data_media_folder"),
		Bind:    r.FormValue("data_media_bind"),
	}
	if v := r.FormValue("data_media_port"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			dm.Port = n
		}
	}
	dm = dm.Normalize()

	if err := s.store.SaveBatchRuntime(rt); err != nil {
		s.render(w, "settings.html", s.settingsPageData(rt, live, dm, sess, err.Error()))
		return
	}
	if err := s.store.SaveSessionSettings(sess); err != nil {
		s.render(w, "settings.html", s.settingsPageData(rt, live, dm, sess, err.Error()))
		return
	}
	if err := s.store.SaveLiveOSSSH(live); err != nil {
		s.render(w, "settings.html", s.settingsPageData(rt, live, dm, sess, err.Error()))
		return
	}
	if err := s.store.SaveDataMedia(dm); err != nil {
		s.render(w, "settings.html", s.settingsPageData(rt, live, dm, sess, err.Error()))
		return
	}
	if s.dataMedia != nil {
		// Prefer Data DHCP interface for BaseURL when bind is wildcard.
		if saved, err := s.store.GetLocalDHCPConfig(); err == nil && saved.Interface != "" {
			s.dataMedia.SetIfaceHint(saved.Interface)
		}
		if err := s.dataMedia.Apply(datamedia.Config{
			Enabled: dm.Enabled, Port: dm.Port, Folder: dm.Folder, Bind: dm.Bind,
		}); err != nil {
			data := s.settingsPageData(rt, live, dm, sess,
				"Settings saved, but data media server failed to start: "+err.Error())
			s.render(w, "settings.html", data)
			return
		}
	}

	// Apply new timeout immediately to this browser session.
	s.issueSessionCookie(w)
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
