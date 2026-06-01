// Package fileserve is the model-serve file server (ADR 018): the
// cluster-canonical model store lives on a PVC; this sidecar serves and ingests
// model files over a token-authenticated HTTP endpoint.
//
//   - GET  /models/<path>     stream a model to a GPU worker (range-resumable;
//     workers verify sha256 their side — no hashing on this hot path).
//   - POST /models/ingest     download a model server-side into the store
//     ({url, dest, sha256?}); streams to a temp file, optionally verifies the
//     sha256, atomically renames into place, and returns the computed sha256.
//     This is the sustainable ingestion path — no shell/exec/scale-down (works
//     in the distroless image because the Go binary does the fetch itself).
package fileserve

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	modelsPrefix = "/models/"
	ingestPath   = "/models/ingest"
)

// Handler returns the model-serve HTTP handler rooted at root, requiring the
// bearer token on every /models request. An empty token is rejected (fail
// closed — a blank token must never mean "auth disabled").
func Handler(root, token string) (http.Handler, error) {
	if token == "" {
		return nil, errors.New("model-serve: empty auth token (refusing to run open)")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	tokenBytes := []byte(token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok\n"))
			return
		}
		if !authorized(r, tokenBytes) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == ingestPath {
			ingestModel(w, r, absRoot)
			return
		}
		serveModel(w, r, absRoot)
	}), nil
}

// confine cleans rel and resolves it under absRoot, rejecting any path that
// escapes the root (traversal / absolute). Shared by serve + ingest.
func confine(absRoot, rel string) (string, bool) {
	clean := path.Clean("/" + rel)
	full := filepath.Join(absRoot, filepath.FromSlash(clean))
	if full != absRoot && !strings.HasPrefix(full, absRoot+string(os.PathSeparator)) {
		return "", false
	}
	return full, true
}

func authorized(r *http.Request, token []byte) bool {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, prefix) {
		return false
	}
	got := []byte(strings.TrimSpace(strings.TrimPrefix(h, prefix)))
	return subtle.ConstantTimeCompare(got, token) == 1
}

func serveModel(w http.ResponseWriter, r *http.Request, absRoot string) {
	if !strings.HasPrefix(r.URL.Path, modelsPrefix) {
		http.NotFound(w, r)
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, modelsPrefix)
	if rel == "" {
		http.NotFound(w, r) // no directory listing of the store root
		return
	}
	// Confine to the served root (r.URL.Path is already %-decoded by net/http).
	full, ok := confine(absRoot, rel)
	if !ok {
		http.NotFound(w, r)
		return
	}

	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		// Missing file OR a directory request → 404 (never enumerate contents).
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	// ServeContent gives range support (resumable worker fetches) + content-type.
	http.ServeContent(w, r, info.Name(), info.ModTime(), f)
}

// ingestReq is the POST /models/ingest body: pull <url> into <dest> (relative to
// the store root), optionally verifying <sha256>.
type ingestReq struct {
	URL    string `json:"url"`
	Dest   string `json:"dest"`
	SHA256 string `json:"sha256,omitempty"`
}

type ingestResp struct {
	Dest   string `json:"dest"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// ingestModel downloads a model server-side into the store (ADR 018 ingestion
// path — no shell/exec/scale-down needed). It streams the source to a temp file
// in the destination dir (hashing as it goes), verifies the sha256 if one was
// given, then atomically renames into place. The computed sha256 is returned so
// the registry can be updated. Confined to the store root; token-gated upstream.
func ingestModel(w http.ResponseWriter, r *http.Request, absRoot string) {
	var req ingestReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	if req.URL == "" || req.Dest == "" {
		http.Error(w, "url and dest are required", http.StatusBadRequest)
		return
	}
	// For a write, reject a malformed dest outright rather than silently
	// relocating it — dest must be a clean relative path under the store.
	if strings.HasPrefix(req.Dest, "/") || strings.Contains(req.Dest, "..") || strings.ContainsAny(req.Dest, "\\") {
		http.Error(w, "dest must be a relative path without '..'", http.StatusBadRequest)
		return
	}
	full, ok := confine(absRoot, req.Dest)
	if !ok || full == absRoot {
		http.Error(w, "unsafe or empty dest", http.StatusBadRequest)
		return
	}

	resp, err := http.Get(req.URL)
	if err != nil {
		http.Error(w, fmt.Sprintf("fetch source: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		http.Error(w, fmt.Sprintf("source returned %d", resp.StatusCode), http.StatusBadGateway)
		return
	}

	dir := filepath.Dir(full)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		http.Error(w, "mkdir dest dir", http.StatusInternalServerError)
		return
	}
	tmp, err := os.CreateTemp(dir, ".ingest-*")
	if err != nil {
		http.Error(w, "create temp", http.StatusInternalServerError)
		return
	}
	tmpName := tmp.Name()
	// Clean up the temp file on any failure path.
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()

	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), resp.Body)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		http.Error(w, fmt.Sprintf("write download: %v", err), http.StatusBadGateway)
		return
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if req.SHA256 != "" && !strings.EqualFold(req.SHA256, sum) {
		http.Error(w, fmt.Sprintf("sha256 mismatch: got %s want %s", sum, req.SHA256), http.StatusUnprocessableEntity)
		return
	}
	if err := os.Rename(tmpName, full); err != nil {
		http.Error(w, "finalise file", http.StatusInternalServerError)
		return
	}
	committed = true

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ingestResp{Dest: req.Dest, Bytes: n, SHA256: sum})
}
