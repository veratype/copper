package media

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"copper_bmc/internal/config"
)

// FileInfo is one downloadable path under the media folder (URL-relative).
type FileInfo struct {
	Path string `json:"path"` // e.g. "ubuntu.iso" or "isos/netboot.iso"
	Size int64  `json:"size"`
}

// Catalog is the OOB media library exposed to copper_control_plane.
type Catalog struct {
	Folder  string     `json:"folder"`
	BaseURL string     `json:"base_url"` // BMC-reachable http://oob-ip:port (empty if unknown)
	Bind    string     `json:"bind"`
	Iface   string     `json:"interface"`
	Files   []FileInfo `json:"files"`
	Note    string     `json:"note,omitempty"`
}

// ListCatalog walks cfg.MediaFolder and builds BMC-facing URLs when bind is concrete.
func ListCatalog(cfg *config.Config) (Catalog, error) {
	out := Catalog{
		Folder: cfg.MediaFolder,
		Bind:   cfg.MediaListener.BindAddr,
		Iface:  cfg.MediaListener.Interface,
		Files:  []FileInfo{},
	}
	out.BaseURL = MediaBaseURL(cfg)
	if out.BaseURL == "" {
		out.Note = "media bind is 0.0.0.0/all — set a concrete OOB NIC/IP so image URLs are BMC-reachable, or enter the base URL manually in the UI"
	}

	root := cfg.MediaFolder
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
		return out, fmt.Errorf("list media folder %s: %w", root, err)
	}
	return out, nil
}

// MediaBaseURL returns http://<oob-ip>:<port> when the media listener has a concrete IPv4.
func MediaBaseURL(cfg *config.Config) string {
	bind := strings.TrimSpace(cfg.MediaListener.BindAddr)
	port := cfg.MediaListener.Port
	if bind != "" && bind != "0.0.0.0" {
		if ip := net.ParseIP(bind); ip != nil && ip.To4() != nil {
			return "http://" + net.JoinHostPort(bind, strconv.Itoa(port))
		}
	}
	// Fallback: concrete NIC name → first IPv4 on that interface.
	ifaceName := strings.TrimSpace(cfg.MediaListener.Interface)
	if ifaceName == "" || ifaceName == "all" || ifaceName == "custom" {
		return ""
	}
	iface, err := net.InterfaceByName(ifaceName)
	if err != nil {
		return ""
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsLoopback() || ip.To4() == nil {
			continue
		}
		return "http://" + net.JoinHostPort(ip.String(), strconv.Itoa(port))
	}
	return ""
}
