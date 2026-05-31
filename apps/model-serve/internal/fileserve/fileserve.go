// Package fileserve is the model-serve read-only file server (ADR 018): the
// cluster-canonical model store lives on a PVC mounted on the ComfyUI master;
// this sidecar streams individual model files to the GPU workers over a
// token-authenticated HTTP endpoint. Workers fetch, cache, and verify the
// sha256 worker-side — this server only authenticates and streams (no hashing
// on the hot path).
package fileserve

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const modelsPrefix = "/models/"

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
		serveModel(w, r, absRoot)
	}), nil
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
	// Clean against a rooted path so any ".." is neutralised, then confine to
	// the served root. r.URL.Path is already %-decoded by net/http.
	clean := path.Clean("/" + rel)
	full := filepath.Join(absRoot, filepath.FromSlash(clean))
	if full != absRoot && !strings.HasPrefix(full, absRoot+string(os.PathSeparator)) {
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
