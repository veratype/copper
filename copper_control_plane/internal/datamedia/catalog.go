package datamedia

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileInfo is one downloadable path under the media folder.
type FileInfo struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256,omitempty"`
}

// Catalog is the data-plane media library for the provision UI.
type Catalog struct {
	Folder  string     `json:"folder"`
	BaseURL string     `json:"base_url"`
	Bind    string     `json:"bind"`
	Running bool       `json:"running"`
	Files   []FileInfo `json:"files"`
	Note    string     `json:"note,omitempty"`
	Error   string     `json:"error,omitempty"`
}

// ListCatalog walks the configured folder.
func (m *Manager) ListCatalog() (Catalog, error) {
	st := m.Status()
	out := Catalog{
		Folder:  st.Config.Folder,
		BaseURL: st.BaseURL,
		Bind:    st.Config.Bind,
		Running: st.Running,
		Files:   []FileInfo{},
		Note:    st.Note,
		Error:   st.Error,
	}
	if strings.TrimSpace(st.Config.Folder) == "" {
		return out, fmt.Errorf("data media folder not configured (Settings)")
	}
	root := st.Config.Folder
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		out.Files = append(out.Files, FileInfo{
			Path: filepath.ToSlash(rel),
			Size: info.Size(),
		})
		return nil
	})
	if err != nil {
		return out, fmt.Errorf("list data media folder %s: %w", root, err)
	}
	return out, nil
}

// FileURL returns a host-reachable URL for a relative path, or empty if BaseURL unknown.
func (m *Manager) FileURL(rel string) string {
	st := m.Status()
	if st.BaseURL == "" {
		return ""
	}
	rel = strings.TrimPrefix(filepath.ToSlash(rel), "/")
	return strings.TrimRight(st.BaseURL, "/") + "/" + rel
}
