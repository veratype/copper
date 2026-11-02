package datamedia

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Digest is a cached content hash for one media file.
type Digest struct {
	SHA256    string    `json:"sha256"`
	Size      int64     `json:"size"`
	ModTime   time.Time `json:"mod_time"`
	Path      string    `json:"path"`
	ComputedAt time.Time `json:"computed_at"`
}

type digestCache struct {
	mu    sync.Mutex
	byKey map[string]Digest // absPath|size|mtimeUnixNano
}

func newDigestCache() *digestCache {
	return &digestCache{byKey: map[string]Digest{}}
}

func (c *digestCache) get(abs string, size int64, mtime time.Time) (Digest, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.byKey[cacheKey(abs, size, mtime)]
	return d, ok
}

func (c *digestCache) put(d Digest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byKey[cacheKey(d.Path, d.Size, d.ModTime)] = d
}

func cacheKey(abs string, size int64, mtime time.Time) string {
	return fmt.Sprintf("%s|%d|%d", abs, size, mtime.UnixNano())
}

// FileDigest returns sha256 of a file under the media folder (relative path).
// Results are cached by path + size + mtime so large images are hashed once.
func (m *Manager) FileDigest(rel string) (Digest, error) {
	m.mu.Lock()
	folder := m.cfg.Folder
	if m.digests == nil {
		m.digests = newDigestCache()
	}
	cache := m.digests
	m.mu.Unlock()

	rel = strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(rel)), "/")
	if rel == "" || strings.Contains(rel, "..") {
		return Digest{}, fmt.Errorf("invalid media path")
	}
	abs := filepath.Join(folder, filepath.FromSlash(rel))
	info, err := os.Stat(abs)
	if err != nil {
		return Digest{}, err
	}
	if info.IsDir() {
		return Digest{}, fmt.Errorf("not a file: %s", rel)
	}
	if d, ok := cache.get(abs, info.Size(), info.ModTime()); ok {
		return d, nil
	}
	sum, err := hashFileSHA256(abs)
	if err != nil {
		return Digest{}, err
	}
	d := Digest{
		SHA256:     sum,
		Size:       info.Size(),
		ModTime:    info.ModTime(),
		Path:       abs,
		ComputedAt: time.Now().UTC(),
	}
	cache.put(d)
	return d, nil
}

// DigestForURLPath hashes the file for an HTTP URL path ("/foo.raw").
func (m *Manager) DigestForURLPath(urlPath string) (Digest, error) {
	return m.FileDigest(strings.TrimPrefix(urlPath, "/"))
}

// CachedDigestForURLPath returns a digest only if already computed (non-blocking).
func (m *Manager) CachedDigestForURLPath(urlPath string) (Digest, bool) {
	m.mu.Lock()
	folder := m.cfg.Folder
	cache := m.digests
	m.mu.Unlock()
	if cache == nil || folder == "" {
		return Digest{}, false
	}
	rel := strings.TrimPrefix(filepath.ToSlash(strings.TrimSpace(urlPath)), "/")
	abs := filepath.Join(folder, filepath.FromSlash(rel))
	info, err := os.Stat(abs)
	if err != nil || info.IsDir() {
		return Digest{}, false
	}
	return cache.get(abs, info.Size(), info.ModTime())
}

// WarmDigests hashes media files in the background so HEAD/provision can reuse them.
func (m *Manager) WarmDigests() {
	go func() {
		cat, err := m.ListCatalog()
		if err != nil {
			return
		}
		for _, f := range cat.Files {
			_, _ = m.FileDigest(f.Path)
		}
	}()
}

func hashFileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
