package fileserve

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

// --- ingest endpoint (POST /models/ingest) ---

func postIngest(h http.Handler, auth, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/models/ingest", strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// originServer serves fixed bytes as a stand-in model source (HF/civitai/etc).
func originServer(t *testing.T, body []byte) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	t.Cleanup(s.Close)
	return s
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestIngestDownloadsStoresAndReturnsSha(t *testing.T) {
	h, root := setup(t, "worker-secret")
	payload := []byte("THE-SDXL-WEIGHTS")
	origin := originServer(t, payload)

	body := `{"url":"` + origin.URL + `","dest":"checkpoints/new.safetensors"}`
	rec := postIngest(h, "worker-secret", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("ingest status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	// File landed at the confined dest with the right content.
	got, err := os.ReadFile(filepath.Join(root, "checkpoints", "new.safetensors"))
	if err != nil || string(got) != string(payload) {
		t.Fatalf("stored file wrong: err=%v got=%q", err, got)
	}
	// Response reports bytes + the computed sha256 (so the registry can be updated).
	var resp struct {
		Dest   string `json:"dest"`
		Bytes  int64  `json:"bytes"`
		SHA256 string `json:"sha256"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("resp not JSON: %v", err)
	}
	if resp.Bytes != int64(len(payload)) || resp.SHA256 != sha256hex(payload) {
		t.Errorf("resp = %+v, want bytes=%d sha=%s", resp, len(payload), sha256hex(payload))
	}
}

func TestIngestVerifiesSha256(t *testing.T) {
	h, root := setup(t, "worker-secret")
	payload := []byte("verify-me")
	origin := originServer(t, payload)

	// Correct sha → stored.
	good := `{"url":"` + origin.URL + `","dest":"checkpoints/ok.bin","sha256":"` + sha256hex(payload) + `"}`
	if rec := postIngest(h, "worker-secret", good); rec.Code != http.StatusOK {
		t.Fatalf("correct sha → %d, want 200 (%s)", rec.Code, rec.Body.String())
	}

	// Wrong sha → rejected AND the bad file must not be left behind.
	bad := `{"url":"` + origin.URL + `","dest":"checkpoints/bad.bin","sha256":"0000000000000000000000000000000000000000000000000000000000000000"}`
	rec := postIngest(h, "worker-secret", bad)
	if rec.Code == http.StatusOK {
		t.Fatalf("sha mismatch should fail, got 200")
	}
	if _, err := os.Stat(filepath.Join(root, "checkpoints", "bad.bin")); !os.IsNotExist(err) {
		t.Errorf("mismatched download must be cleaned up, but bad.bin exists")
	}
}

func TestIngestOptionalSha(t *testing.T) {
	h, _ := setup(t, "worker-secret")
	origin := originServer(t, []byte("no-sha-needed"))
	body := `{"url":"` + origin.URL + `","dest":"checkpoints/nosha.bin"}`
	if rec := postIngest(h, "worker-secret", body); rec.Code != http.StatusOK {
		t.Fatalf("omitted sha should still succeed, got %d (%s)", rec.Code, rec.Body.String())
	}
}

func TestIngestRejectsTraversalDest(t *testing.T) {
	h, root := setup(t, "worker-secret")
	origin := originServer(t, []byte("evil"))
	for _, dest := range []string{"../escape.bin", "../../etc/cron", "/abs/path.bin"} {
		body := `{"url":"` + origin.URL + `","dest":"` + dest + `"}`
		rec := postIngest(h, "worker-secret", body)
		if rec.Code == http.StatusOK {
			t.Errorf("traversal dest %q accepted", dest)
		}
	}
	// Nothing escaped the root.
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "escape.bin")); err == nil {
		t.Errorf("traversal wrote outside root")
	}
}

func TestIngestRequiresToken(t *testing.T) {
	h, _ := setup(t, "worker-secret")
	origin := originServer(t, []byte("x"))
	body := `{"url":"` + origin.URL + `","dest":"checkpoints/x.bin"}`
	if rec := postIngest(h, "", body); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token → %d, want 401", rec.Code)
	}
	if rec := postIngest(h, "wrong", body); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token → %d, want 401", rec.Code)
	}
}

// gatedOriginServer returns body only when the request carries the expected
// Authorization header; otherwise responds 401. Stands in for HuggingFace
// gated repos / private registries.
func gatedOriginServer(t *testing.T, body []byte, expectedAuth string) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != expectedAuth {
			http.Error(w, "gated", http.StatusUnauthorized)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestIngestForwardsAuthHeaderToSource(t *testing.T) {
	h, root := setup(t, "worker-secret")
	payload := []byte("THE-GATED-WEIGHTS")
	origin := gatedOriginServer(t, payload, "Bearer hf_test_token")

	// Without auth → source returns 401, model-serve returns 502.
	noAuth := `{"url":"` + origin.URL + `","dest":"checkpoints/gated.bin"}`
	if rec := postIngest(h, "worker-secret", noAuth); rec.Code != http.StatusBadGateway {
		t.Fatalf("gated source without auth → %d, want 502", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(root, "checkpoints", "gated.bin")); !os.IsNotExist(err) {
		t.Errorf("failed gated fetch must not leave a file")
	}

	// With auth → source returns body, model-serve stores it.
	withAuth := `{"url":"` + origin.URL + `","dest":"checkpoints/gated.bin","auth":"Bearer hf_test_token"}`
	rec := postIngest(h, "worker-secret", withAuth)
	if rec.Code != http.StatusOK {
		t.Fatalf("gated source with correct auth → %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	got, err := os.ReadFile(filepath.Join(root, "checkpoints", "gated.bin"))
	if err != nil || string(got) != string(payload) {
		t.Fatalf("stored file wrong: err=%v got=%q", err, got)
	}
}
