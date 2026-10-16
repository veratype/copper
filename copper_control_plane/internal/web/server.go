package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"copper_control_plane/internal/agent"
	"copper_control_plane/internal/config"
	"copper_control_plane/internal/store"
)

type Server struct {
	cfg      *config.Config
	store    *store.Store
	agent    *agent.Client
	pages    map[string]*template.Template
	staticFS fs.FS
}

type pageData struct {
	Title           string
	Authed          bool
	Flash           string
	Error           string
	AgentURL        string
	Servers         []store.Server
	Server          *store.Server
	Result          string
	ResultKind      string // inventory | boot | virtual-media | reset | mount
	ResultFetchedAt string // RFC3339 / display time for cached snapshots
}

func New(cfg *config.Config, st *store.Store, ag *agent.Client, templatesFS, staticFS fs.FS) (*Server, error) {
	pages := map[string]*template.Template{}
	for _, name := range []string{"login.html", "servers.html", "server_form.html", "server_detail.html"} {
		t, err := template.ParseFS(templatesFS, "templates/layout.html", "templates/"+name)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		pages[name] = t
	}
	return &Server{cfg: cfg, store: st, agent: ag, pages: pages, staticFS: staticFS}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(s.staticFS))))

	mux.HandleFunc("GET /login", s.handleLoginGet)
	mux.HandleFunc("POST /login", s.handleLoginPost)
	mux.HandleFunc("GET /logout", s.handleLogout)

	mux.Handle("GET /{$}", s.auth(s.handleIndex))
	mux.Handle("GET /servers/new", s.auth(s.handleServerNewGet))
	mux.Handle("POST /servers/new", s.auth(s.handleServerNewPost))
	mux.Handle("GET /servers/{id}", s.auth(s.handleServerDetail))
	mux.Handle("GET /servers/{id}/edit", s.auth(s.handleServerEditGet))
	mux.Handle("POST /servers/{id}/edit", s.auth(s.handleServerEditPost))
	mux.Handle("POST /servers/{id}/delete", s.auth(s.handleServerDelete))

	mux.Handle("POST /servers/{id}/actions/inventory", s.auth(s.handleActionInventory))
	mux.Handle("POST /servers/{id}/actions/boot", s.auth(s.handleActionBoot))
	mux.Handle("POST /servers/{id}/actions/virtual-media", s.auth(s.handleActionVirtualMedia))
	mux.Handle("POST /servers/{id}/actions/reset", s.auth(s.handleActionReset))
	mux.Handle("POST /servers/{id}/actions/mount", s.auth(s.handleActionMount))
	// Same payload as Inventory action; Content-Disposition for browser download + curl -OJ.
	mux.Handle("GET /servers/{id}/export/inventory.json", s.auth(s.handleExportInventory))

	return mux
}

func (s *Server) auth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.isAuthed(r) {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	})
}

func (s *Server) handleLoginGet(w http.ResponseWriter, r *http.Request) {
	if s.isAuthed(r) {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	s.render(w, "login.html", pageData{Title: "Login"})
}

func (s *Server) handleLoginPost(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	if r.FormValue("password") != s.cfg.MasterPassword {
		s.render(w, "login.html", pageData{Title: "Login", Error: "Invalid master password"})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "copper_session",
		Value:    s.sessionToken(),
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(12 * time.Hour),
	})
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "copper_session", Value: "", Path: "/", MaxAge: -1})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	servers, err := s.store.ListServers()
	if err != nil {
		s.render(w, "servers.html", pageData{Title: "Servers", Authed: true, Error: err.Error(), AgentURL: s.cfg.AgentBaseURL()})
		return
	}
	s.render(w, "servers.html", pageData{
		Title:    "Servers",
		Authed:   true,
		AgentURL: s.cfg.AgentBaseURL(),
		Servers:  servers,
	})
}

func (s *Server) handleServerNewGet(w http.ResponseWriter, _ *http.Request) {
	s.render(w, "server_form.html", pageData{Title: "Add server", Authed: true})
}

func (s *Server) handleServerNewPost(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	srv := store.Server{
		Name:     strings.TrimSpace(r.FormValue("name")),
		Endpoint: strings.TrimSpace(r.FormValue("endpoint")),
		Username: strings.TrimSpace(r.FormValue("username")),
		Password: r.FormValue("password"),
		Insecure: r.FormValue("insecure") == "1",
		Notes:    strings.TrimSpace(r.FormValue("notes")),
	}
	if srv.Name == "" || srv.Endpoint == "" || srv.Username == "" || srv.Password == "" {
		s.render(w, "server_form.html", pageData{Title: "Add server", Authed: true, Error: "Name, endpoint, username and password are required", Server: &srv})
		return
	}
	id, err := s.store.CreateServer(srv)
	if err != nil {
		s.render(w, "server_form.html", pageData{Title: "Add server", Authed: true, Error: err.Error(), Server: &srv})
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/servers/%d", id), http.StatusSeeOther)
}

func (s *Server) handleServerDetail(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	data := pageData{Title: srv.Name, Authed: true, Server: srv}
	if snap, err := s.store.GetSnapshot(srv.ID, "inventory"); err == nil && snap != nil {
		data.Result = snap.Payload
		data.ResultKind = "inventory"
		data.ResultFetchedAt = snap.FetchedAt.Local().Format("2006-01-02 15:04:05 MST")
	}
	s.render(w, "server_detail.html", data)
}

func (s *Server) handleServerEditGet(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	s.render(w, "server_form.html", pageData{Title: "Edit server", Authed: true, Server: srv})
}

func (s *Server) handleServerEditPost(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	_ = r.ParseForm()
	srv.Name = strings.TrimSpace(r.FormValue("name"))
	srv.Endpoint = strings.TrimSpace(r.FormValue("endpoint"))
	srv.Username = strings.TrimSpace(r.FormValue("username"))
	if pw := r.FormValue("password"); pw != "" {
		srv.Password = pw
	}
	srv.Insecure = r.FormValue("insecure") == "1"
	srv.Notes = strings.TrimSpace(r.FormValue("notes"))
	if err := s.store.UpdateServer(*srv); err != nil {
		s.render(w, "server_form.html", pageData{Title: "Edit server", Authed: true, Server: srv, Error: err.Error()})
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/servers/%d", srv.ID), http.StatusSeeOther)
}

func (s *Server) handleServerDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_ = s.store.DeleteServer(id)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleActionInventory(w http.ResponseWriter, r *http.Request) {
	s.runAction(w, r, "inventory", true, func(srv *store.Server) (json.RawMessage, error) {
		return s.agent.Inventory(srv)
	})
}
func (s *Server) handleActionBoot(w http.ResponseWriter, r *http.Request) {
	s.runAction(w, r, "boot", false, func(srv *store.Server) (json.RawMessage, error) { return s.agent.Boot(srv) })
}
func (s *Server) handleActionVirtualMedia(w http.ResponseWriter, r *http.Request) {
	s.runAction(w, r, "virtual-media", false, func(srv *store.Server) (json.RawMessage, error) {
		return s.agent.VirtualMedia(srv)
	})
}
func (s *Server) handleActionReset(w http.ResponseWriter, r *http.Request) {
	s.runAction(w, r, "reset", false, func(srv *store.Server) (json.RawMessage, error) {
		return s.agent.Reset(srv, map[string]string{"reset_type": "ForceRestart"})
	})
}
func (s *Server) handleActionMount(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	body := map[string]any{
		"image_url": strings.TrimSpace(r.FormValue("image_url")),
		"boot_once": r.FormValue("boot_once") == "1",
		"reboot":    r.FormValue("reboot") == "1",
	}
	s.runAction(w, r, "mount", false, func(srv *store.Server) (json.RawMessage, error) {
		return s.agent.MountMedia(srv, body)
	})
}

// handleExportInventory serves the last cached inventory from SQLite (no Redfish call).
// Run Inventory first to refresh the cache. Suitable for serial curl downloads.
func (s *Server) handleExportInventory(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	snap, err := s.store.GetSnapshot(srv.ID, "inventory")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if snap == nil {
		http.Error(w, "no cached inventory; click Inventory first", http.StatusNotFound)
		return
	}
	name := sanitizeFilename(srv.Name)
	if name == "" {
		name = fmt.Sprintf("server-%d", srv.ID)
	}
	filename := name + "-inventory.json"
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Copper-Fetched-At", snap.FetchedAt.UTC().Format(time.RFC3339))
	_, _ = w.Write([]byte(snap.Payload))
	if !strings.HasSuffix(snap.Payload, "\n") {
		_, _ = w.Write([]byte("\n"))
	}
}

func (s *Server) runAction(w http.ResponseWriter, r *http.Request, kind string, persist bool, fn func(*store.Server) (json.RawMessage, error)) {
	srv, err := s.serverFromPath(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	raw, err := fn(srv)
	if err != nil {
		s.render(w, "server_detail.html", pageData{Title: srv.Name, Authed: true, Server: srv, Error: err.Error()})
		return
	}

	formatted := string(raw)
	var parsed any
	if err := json.Unmarshal(raw, &parsed); err == nil {
		if pretty, err := json.MarshalIndent(parsed, "", "  "); err == nil {
			formatted = string(pretty)
		}
	}

	fetchedAt := ""
	if persist {
		if err := s.store.SaveSnapshot(srv.ID, kind, formatted); err != nil {
			s.render(w, "server_detail.html", pageData{
				Title: srv.Name, Authed: true, Server: srv,
				Error: "inventory ok but failed to cache: " + err.Error(),
				Result: formatted, ResultKind: kind,
			})
			return
		}
		fetchedAt = time.Now().Local().Format("2006-01-02 15:04:05 MST")
	}

	s.render(w, "server_detail.html", pageData{
		Title: srv.Name, Authed: true, Server: srv,
		Result: formatted, ResultKind: kind, ResultFetchedAt: fetchedAt,
	})
}

func sanitizeFilename(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-._")
}

func (s *Server) serverFromPath(r *http.Request) (*store.Server, error) {
	id, err := pathID(r)
	if err != nil {
		return nil, err
	}
	return s.store.GetServer(id)
}

func pathID(r *http.Request) (int64, error) {
	return strconv.ParseInt(r.PathValue("id"), 10, 64)
}

func (s *Server) render(w http.ResponseWriter, page string, data pageData) {
	t := s.pages[page]
	if t == nil {
		http.Error(w, "template missing", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := t.ExecuteTemplate(w, "layout", data); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

func (s *Server) isAuthed(r *http.Request) bool {
	c, err := r.Cookie("copper_session")
	if err != nil {
		return false
	}
	return hmac.Equal([]byte(c.Value), []byte(s.sessionToken()))
}

func (s *Server) sessionToken() string {
	mac := hmac.New(sha256.New, []byte(s.cfg.MasterPassword))
	_, _ = mac.Write([]byte("copper-control-plane-session-v1"))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
