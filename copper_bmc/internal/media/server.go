package media

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"copper_bmc/internal/activity"
	"copper_bmc/internal/config"
)

// Server serves virtual-media files on the OOB / BMC network plane.
type Server struct {
	cfg *config.Config
	act *activity.Buffer
}

func New(cfg *config.Config, act *activity.Buffer) *Server {
	return &Server{cfg: cfg, act: act}
}

func (s *Server) Handler() http.Handler {
	root := http.Dir(s.cfg.MediaFolder)
	fs := http.FileServer(root)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		s.act.Info("media", "%s %s %s", r.RemoteAddr, r.Method, r.URL.Path)

		w.Header().Set("Cache-Control", "no-store")
		if isDownloadableFile(s.cfg.MediaFolder, r.URL.Path) {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("X-Content-Type-Options", "nosniff")
		}
		fs.ServeHTTP(w, r)
	})
	return mux
}

func (s *Server) ListenAndServe() error {
	ln, err := net.Listen("tcp", s.cfg.MediaListener.Addr())
	if err != nil {
		return fmt.Errorf("media listen on %s: %w", s.cfg.MediaListener.Addr(), err)
	}
	s.act.Info("media", "serving %s on http://%s/ (OOB/BMC plane, NIC=%s)",
		s.cfg.MediaFolder, s.cfg.MediaListener.Addr(), s.cfg.MediaListener.Interface)
	return http.Serve(ln, s.Handler())
}

func isDownloadableFile(folder, urlPath string) bool {
	rel := strings.TrimPrefix(urlPath, "/")
	if rel == "" {
		return false
	}
	full := filepath.Join(folder, filepath.FromSlash(rel))
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		return false
	}
	return true
}
