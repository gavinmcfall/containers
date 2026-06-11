package fileserve

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// setupTiered builds a store with a registry: one heavy-only checkpoint, one
// any-tier checkpoint, and an aux file not in the registry at all.
func setupTiered(t *testing.T, token string) (http.Handler, string) {
	t.Helper()
	root := t.TempDir()
	mk := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("checkpoints/heavy-only.safetensors", "HEAVYBYTES")
	mk("checkpoints/anytier.safetensors", "ANYBYTES")
	mk("ultralytics/bbox/face_yolov8m.pt", "AUXBYTES")

	reg := filepath.Join(t.TempDir(), "registry.json")
	regJSON := `[
	  {"filename":"heavy-only.safetensors","licence":"x","commercial_ok":true,"tier_fit":["heavy"]},
	  {"filename":"anytier.safetensors","licence":"x","commercial_ok":true,"tier_fit":["heavy","light"]}
	]`
	if err := os.WriteFile(reg, []byte(regJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := New(root, token, reg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return h, root
}

func manifestPaths(t *testing.T, h http.Handler, url, token string) map[string]bool {
	t.Helper()
	rec := get(h, url, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("manifest status %d: %s", rec.Code, rec.Body.String())
	}
	var entries []struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &entries); err != nil {
		t.Fatalf("manifest not JSON array: %v", err)
	}
	out := map[string]bool{}
	for _, e := range entries {
		if e.Size <= 0 {
			t.Errorf("entry %s has size %d", e.Path, e.Size)
		}
		out[e.Path] = true
	}
	return out
}

func TestManifestUnfilteredListsEverything(t *testing.T) {
	h, _ := setupTiered(t, "tok")
	got := manifestPaths(t, h, "/models/manifest", "tok")
	for _, want := range []string{"checkpoints/heavy-only.safetensors", "checkpoints/anytier.safetensors", "ultralytics/bbox/face_yolov8m.pt"} {
		if !got[want] {
			t.Errorf("missing %s in %v", want, got)
		}
	}
}

func TestManifestTierFiltersRegistryCheckpoints(t *testing.T) {
	h, _ := setupTiered(t, "tok")
	got := manifestPaths(t, h, "/models/manifest?tier=light", "tok")
	if got["checkpoints/heavy-only.safetensors"] {
		t.Errorf("light tier must NOT receive heavy-only checkpoint")
	}
	if !got["checkpoints/anytier.safetensors"] {
		t.Errorf("light tier should receive any-tier checkpoint")
	}
	if !got["ultralytics/bbox/face_yolov8m.pt"] {
		t.Errorf("aux (non-registry) files must ALWAYS be included")
	}
}

func TestManifestRequiresToken(t *testing.T) {
	h, _ := setupTiered(t, "tok")
	if rec := get(h, "/models/manifest", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token should 401, got %d", rec.Code)
	}
}

func TestSha256EndpointComputesAndCaches(t *testing.T) {
	h, root := setupTiered(t, "tok")
	want := sha256.Sum256([]byte("AUXBYTES"))
	wantHex := hex.EncodeToString(want[:])

	for i := 0; i < 2; i++ { // second call exercises the cache path
		rec := get(h, "/models/sha256/ultralytics/bbox/face_yolov8m.pt", "tok")
		if rec.Code != http.StatusOK {
			t.Fatalf("call %d status %d: %s", i, rec.Code, rec.Body.String())
		}
		var resp struct {
			SHA256 string `json:"sha256"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.SHA256 != wantHex {
			t.Errorf("call %d sha = %s want %s", i, resp.SHA256, wantHex)
		}
	}
	// Cache file persisted on the store (survives restarts).
	if _, err := os.Stat(filepath.Join(root, ".sha256-cache.json")); err != nil {
		t.Errorf("persistent cache file missing: %v", err)
	}
}

func TestSha256Missing404(t *testing.T) {
	h, _ := setupTiered(t, "tok")
	if rec := get(h, "/models/sha256/checkpoints/nope.safetensors", "tok"); rec.Code != http.StatusNotFound {
		t.Errorf("missing file should 404, got %d", rec.Code)
	}
}

func TestManifestExcludesCacheFile(t *testing.T) {
	h, _ := setupTiered(t, "tok")
	_ = get(h, "/models/sha256/ultralytics/bbox/face_yolov8m.pt", "tok") // creates cache file
	got := manifestPaths(t, h, "/models/manifest", "tok")
	if got[".sha256-cache.json"] {
		t.Errorf("manifest must not list the internal cache file")
	}
}

func TestManifestTierCaseInsensitive(t *testing.T) {
	// A worker conf with TIER=HEAVY must behave like heavy, not silently match
	// nothing (which strips every registry checkpoint from the manifest).
	h, _ := setupTiered(t, "tok")
	got := manifestPaths(t, h, "/models/manifest?tier=HEAVY", "tok")
	if !got["checkpoints/heavy-only.safetensors"] {
		t.Errorf("tier=HEAVY should match tier_fit 'heavy'; got %v", got)
	}
}
