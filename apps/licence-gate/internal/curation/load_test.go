package curation

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gavinmcfall/containers/apps/licence-gate/internal/identity"
)

func TestLoad_ReadsManifestAndFiles(t *testing.T) {
	dir := t.TempDir()
	wf := []byte(`{"nodes":[],"curated":true}`)
	if err := os.WriteFile(filepath.Join(dir, "portrait.workflow.json"), wf, 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := `{
	  "entries": [
	    {"path": "workflows/lighthouse/portrait.json", "file": "portrait.workflow.json", "role_allowlist": ["family-adult"]}
	  ]
	}`
	if err := os.WriteFile(filepath.Join(dir, "curation.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	s, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	e, ok := s.Lookup("workflows/lighthouse/portrait.json", adult())
	if !ok {
		t.Fatalf("loaded entry not resolvable")
	}
	if string(e.Content) != string(wf) {
		t.Errorf("content = %s, want %s", e.Content, wf)
	}
	if e.Size != len(wf) {
		t.Errorf("size = %d, want %d", e.Size, len(wf))
	}
	if e.ModifiedMS <= 0 {
		t.Errorf("modified not set from file mtime: %d", e.ModifiedMS)
	}
}

func TestLoad_EmptyDirDisablesCuration(t *testing.T) {
	// No path configured -> inert (Empty), not an error.
	s, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\") should be inert, got err %v", err)
	}
	if !s.Empty() {
		t.Errorf("Load(\"\") should yield an empty set")
	}
}

func TestLoad_MissingFileIsError(t *testing.T) {
	dir := t.TempDir()
	manifest := `{"entries":[{"path":"workflows/x.json","file":"absent.json","role_allowlist":[]}]}`
	if err := os.WriteFile(filepath.Join(dir, "curation.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Errorf("Load should fail when a manifest file is missing (fail loud at startup)")
	}
}

func TestLoad_NoManifestIsInert(t *testing.T) {
	// A configured dir with no curation.json (e.g. configMap not yet populated) is
	// inert rather than fatal, so the proxy still boots.
	dir := t.TempDir()
	s, err := Load(dir)
	if err != nil {
		t.Fatalf("missing manifest should be inert, got %v", err)
	}
	if !s.Empty() {
		t.Errorf("missing manifest should yield empty set")
	}
	_ = identity.Identity{}
}
