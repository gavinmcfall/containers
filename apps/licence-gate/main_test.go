package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadResourcesReadsRegistryAndAllowlist(t *testing.T) {
	dir := t.TempDir()
	regPath := filepath.Join(dir, "registry.json")
	allowPath := filepath.Join(dir, "allowlist.json")
	if err := os.WriteFile(regPath, []byte(`[{"filename":"m.safetensors","commercial_ok":true,"licence":"Apache-2.0"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(allowPath, []byte(`{"SaveImage":{"output_field":"filename_prefix"}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	reg, allow, err := loadResources(regPath, allowPath)
	if err != nil {
		t.Fatalf("loadResources: %v", err)
	}
	if e, ok := reg.Resolve("m.safetensors"); !ok || !e.CommercialOK {
		t.Errorf("registry not loaded: %+v ok=%v", e, ok)
	}
	if _, ok := allow["SaveImage"]; !ok {
		t.Errorf("allowlist not loaded: %v", allow)
	}
}

// An absent allowlist path falls back to the built-in DefaultAllowlist so the
// proxy is never left fail-open with an empty (deny-everything-but-also-load-
// nothing) allowlist by a missing optional mount.
func TestLoadResourcesAllowlistFallsBackToDefault(t *testing.T) {
	dir := t.TempDir()
	regPath := filepath.Join(dir, "registry.json")
	if err := os.WriteFile(regPath, []byte(`[]`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, allow, err := loadResources(regPath, filepath.Join(dir, "does-not-exist.json"))
	if err != nil {
		t.Fatalf("missing allowlist should fall back, got err: %v", err)
	}
	if _, ok := allow["CheckpointLoaderSimple"]; !ok {
		t.Errorf("fallback allowlist missing intrinsic node: %v", allow)
	}
}

// A missing registry is fatal — the gate cannot make commercial decisions
// without it, so we must not start.
func TestLoadResourcesMissingRegistryIsError(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := loadResources(filepath.Join(dir, "nope.json"), ""); err == nil {
		t.Fatal("missing registry must be an error")
	}
}

func TestEnvOrDefault(t *testing.T) {
	t.Setenv("LIGHTHOUSE_TEST_KEY", "set-value")
	if got := envOr("LIGHTHOUSE_TEST_KEY", "fallback"); got != "set-value" {
		t.Errorf("envOr set = %q, want set-value", got)
	}
	if got := envOr("LIGHTHOUSE_UNSET_KEY_XYZ", "fallback"); got != "fallback" {
		t.Errorf("envOr unset = %q, want fallback", got)
	}
}
