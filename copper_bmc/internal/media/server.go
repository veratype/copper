package media

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

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
		start := time.Now()
		path := r.URL.Path
		rangeHdr := r.Header.Get("Range")
		ua := r.Header.Get("User-Agent")

		s.act.Info("media", "REQ from=%s proto=%s %s %q host=%q range=%q ua=%q",
			r.RemoteAddr, r.Proto, r.Method, path, r.Host, emptyDash(rangeHdr), truncate(ua, 80))

		if strings.Contains(path, "//") {
			s.act.Warn("media", "path has double slash (BMC clients often fail on redirect): %q", path)
		}
		if strings.HasSuffix(path, "/") && path != "/" {
			s.act.Warn("media", "path ends with / — FileServer may 301-redirect; BMCs rarely follow redirects: %q", path)
		}

		rw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		w.Header().Set("Cache-Control", "no-store")
		if isDownloadableFile(s.cfg.MediaFolder, path) {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("X-Content-Type-Options", "nosniff")
		}
		fs.ServeHTTP(rw, r)

		loc := rw.Header().Get("Location")
		cl := rw.Header().Get("Content-Length")
		cr := rw.Header().Get("Content-Range")
		ar := rw.Header().Get("Accept-Ranges")
		s.act.Info("media", "RESP from=%s %s %q → %d bytes=%d cl=%q cr=%q ar=%q loc=%q dur=%s",
			r.RemoteAddr, r.Method, path, rw.status, rw.bytes,
			emptyDash(cl), emptyDash(cr), emptyDash(ar), emptyDash(loc),
			time.Since(start).Round(time.Millisecond))

		if rw.status == http.StatusMovedPermanently || rw.status == http.StatusFound ||
			rw.status == http.StatusTemporaryRedirect || rw.status == http.StatusPermanentRedirect {
			s.act.Warn("media", "redirect %d → %q — BMC firmware usually does not follow redirects", rw.status, loc)
		}
	})
	return mux
}

func (s *Server) ListenAndServe() error {
	want := s.cfg.MediaListener.Addr()
	ln, err := net.Listen("tcp", want)
	if err != nil {
		return fmt.Errorf("media listen on %s: %w", want, err)
	}
	actual := ln.Addr().String()
	base := MediaBaseURL(s.cfg)
	s.act.Info("media", "LISTEN requested=%s actual=%s nic=%q folder=%q",
		want, actual, s.cfg.MediaListener.Interface, s.cfg.MediaFolder)
	if base != "" {
		s.act.Info("media", "BMC-facing base_url=%s (image_url must use this IP, not localhost)", base)
	} else {
		s.act.Warn("media", "bind is %s — no concrete BMC-facing base_url; BMCs must be pointed at a real OOB IP on this host", want)
	}
	s.act.Info("media", "waiting for BMC HTTP GET/HEAD (Range) — if mount runs but no REQ lines appear, traffic is not reaching this listener")

	srv := &http.Server{
		Handler: s.Handler(),
		ConnState: func(c net.Conn, st http.ConnState) {
			switch st {
			case http.StateNew:
				s.act.Info("media", "TCP accept remote=%s local=%s", c.RemoteAddr(), c.LocalAddr())
			case http.StateClosed:
				// keep quiet — paired with RESP / idle close noise
			}
		},
	}
	return srv.Serve(ln)
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

type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	n, err := r.ResponseWriter.Write(p)
	r.bytes += n
	return n, err
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
