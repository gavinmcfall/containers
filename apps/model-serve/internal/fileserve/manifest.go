package fileserve

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

const (
	manifestPath  = "/models/manifest"
	sha256Prefix  = "/models/sha256/"
	hashCacheFile = ".sha256-cache.json"
)

// New returns the model-serve handler with the worker-sync endpoints enabled
// (ADR 018 auto-fetch): GET /models/manifest[?tier=] and GET /models/sha256/<path>,
// alongside the existing serve + ingest routes. registryPath points at the
// licence registry JSON used for tier filtering; empty disables filtering (the
// manifest then always lists everything).
func New(root, token, registryPath string) (http.Handler, error) {
	base, err := Handler(root, token)
	if err != nil {
		return nil, err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	tiers, err := loadTierFit(registryPath)
	if err != nil {
		return nil, err
	}
	hc := &hashCache{path: filepath.Join(absRoot, hashCacheFile), entries: map[string]hashEntry{}}
	hc.load()
	tokenBytes := []byte(token)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == manifestPath:
			if !authorized(r, tokenBytes) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			serveManifest(w, r, absRoot, tiers)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, sha256Prefix):
			if !authorized(r, tokenBytes) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			serveSha256(w, r, absRoot, hc)
		default:
			base.ServeHTTP(w, r)
		}
	}), nil
}

// loadTierFit reads the licence registry and returns filename → tier_fit. A
// missing/empty path is allowed (no filtering); a present-but-unreadable or
// malformed registry is a startup error (fail loud, not silently unfiltered).
func loadTierFit(path string) (map[string][]string, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil // optional mount not present — serve unfiltered
		}
		return nil, err
	}
	var entries []struct {
		Filename string   `json:"filename"`
		TierFit  []string `json:"tier_fit"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, err
	}
	m := make(map[string][]string, len(entries))
	for _, e := range entries {
		if e.Filename != "" {
			m[e.Filename] = e.TierFit
		}
	}
	return m, nil
}

type manifestEntry struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Mtime int64  `json:"mtime"`
}

// serveManifest walks the store and lists files, tier-filtered: a file whose
// basename appears in the registry is included only if the requested tier is in
// its tier_fit (no tier param = include all); files NOT in the registry (aux
// models — detectors, upscalers, clip-vision, …) are always included.
func serveManifest(w http.ResponseWriter, r *http.Request, absRoot string, tiers map[string][]string) {
	tier := strings.TrimSpace(r.URL.Query().Get("tier"))
	var out []manifestEntry
	_ = filepath.WalkDir(absRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") {
			return nil // internal files (hash cache, ingest temps) never listed
		}
		rel, err := filepath.Rel(absRoot, p)
		if err != nil {
			return nil
		}
		if tier != "" && tiers != nil {
			if fit, known := tiers[name]; known && !contains(fit, tier) {
				return nil // registry checkpoint the requested tier can't run
			}
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		out = append(out, manifestEntry{Path: filepath.ToSlash(rel), Size: info.Size(), Mtime: info.ModTime().Unix()})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	if out == nil {
		out = []manifestEntry{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// contains is case-insensitive: a worker conf saying TIER=HEAVY must match
// tier_fit "heavy" rather than silently stripping every registry checkpoint
// from the manifest (bit the fleet install, 2026-06-12).
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// hashCache is the persistent sha256 cache: hashing multi-GB models is
// expensive (CephFS reads), so each file is hashed once per (size, mtime) and
// remembered across restarts in a JSON file at the store root.
type hashEntry struct {
	Size   int64  `json:"size"`
	Mtime  int64  `json:"mtime"`
	SHA256 string `json:"sha256"`
}

type hashCache struct {
	mu      sync.Mutex
	path    string
	entries map[string]hashEntry
}

func (c *hashCache) load() {
	raw, err := os.ReadFile(c.path)
	if err != nil {
		return // first run / unreadable → start empty
	}
	_ = json.Unmarshal(raw, &c.entries)
	if c.entries == nil {
		c.entries = map[string]hashEntry{}
	}
}

func (c *hashCache) persist() {
	raw, err := json.Marshal(c.entries)
	if err != nil {
		return
	}
	_ = os.WriteFile(c.path, raw, 0o644) // best-effort; cache is reconstructible
}

// serveSha256 returns {"sha256": ...} for one store file, computing + caching on
// first request (keyed by size+mtime so a replaced file re-hashes).
func serveSha256(w http.ResponseWriter, r *http.Request, absRoot string, hc *hashCache) {
	rel := strings.TrimPrefix(r.URL.Path, sha256Prefix)
	full, ok := confine(absRoot, rel)
	if !ok {
		http.NotFound(w, r)
		return
	}
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}

	hc.mu.Lock()
	cached, hit := hc.entries[rel]
	hc.mu.Unlock()
	if hit && cached.Size == info.Size() && cached.Mtime == info.ModTime().Unix() {
		writeSha(w, cached.SHA256)
		return
	}

	f, err := os.Open(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		http.Error(w, "hash failed", http.StatusInternalServerError)
		return
	}
	sum := hex.EncodeToString(h.Sum(nil))

	hc.mu.Lock()
	hc.entries[rel] = hashEntry{Size: info.Size(), Mtime: info.ModTime().Unix(), SHA256: sum}
	hc.persist()
	hc.mu.Unlock()
	writeSha(w, sum)
}

func writeSha(w http.ResponseWriter, sum string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"sha256": sum})
}
