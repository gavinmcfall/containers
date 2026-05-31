package fileserve

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// setup builds a model store with one file in a subfolder and returns a handler.
func setup(t *testing.T, token string) (http.Handler, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "checkpoints"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "checkpoints", "sdxl.safetensors"), []byte("MODELBYTES"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A secret file outside the served root, to prove traversal can't reach it.
	parent := filepath.Dir(root)
	_ = os.WriteFile(filepath.Join(parent, "secret.txt"), []byte("TOPSECRET"), 0o644)

	h, err := Handler(root, token)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	return h, root
}

func get(h http.Handler, path, auth string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestServesFileWithValidToken(t *testing.T) {
	h, _ := setup(t, "worker-secret")
	rec := get(h, "/models/checkpoints/sdxl.safetensors", "worker-secret")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "MODELBYTES" {
		t.Errorf("body = %q, want MODELBYTES", rec.Body.String())
	}
}

func TestRejectsMissingToken(t *testing.T) {
	h, _ := setup(t, "worker-secret")
	rec := get(h, "/models/checkpoints/sdxl.safetensors", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token → status = %d, want 401", rec.Code)
	}
}

func TestRejectsWrongToken(t *testing.T) {
	h, _ := setup(t, "worker-secret")
	rec := get(h, "/models/checkpoints/sdxl.safetensors", "guessed")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token → status = %d, want 401", rec.Code)
	}
}

func TestEmptyConfiguredTokenRejectsAll(t *testing.T) {
	// A blank token must NOT mean "auth disabled" — it must fail closed.
	if _, err := Handler(t.TempDir(), ""); err == nil {
		t.Fatal("empty token must be a configuration error (fail closed)")
	}
}

func TestPathTraversalBlocked(t *testing.T) {
	h, _ := setup(t, "worker-secret")
	for _, p := range []string{
		"/models/../secret.txt",
		"/models/checkpoints/../../secret.txt",
		"/models/..%2f..%2fsecret.txt",
	} {
		rec := get(h, p, "worker-secret")
		if rec.Code == http.StatusOK && rec.Body.String() == "TOPSECRET" {
			t.Errorf("traversal %q leaked the secret file", p)
		}
		if rec.Code != http.StatusNotFound && rec.Code != http.StatusBadRequest {
			t.Errorf("traversal %q → status %d, want 404/400", p, rec.Code)
		}
	}
}

func TestNonexistentFile404(t *testing.T) {
	h, _ := setup(t, "worker-secret")
	rec := get(h, "/models/checkpoints/nope.safetensors", "worker-secret")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing file → status = %d, want 404", rec.Code)
	}
}

func TestNoDirectoryListing(t *testing.T) {
	h, _ := setup(t, "worker-secret")
	// Requesting a directory must not enumerate the model inventory.
	for _, p := range []string{"/models/", "/models/checkpoints/", "/models/checkpoints"} {
		rec := get(h, p, "worker-secret")
		if rec.Code == http.StatusOK {
			t.Errorf("directory %q listed (code 200): %q", p, rec.Body.String())
		}
	}
}

func TestHealthzNoAuth(t *testing.T) {
	h, _ := setup(t, "worker-secret")
	rec := get(h, "/healthz", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz → status = %d, want 200 (no auth)", rec.Code)
	}
}
