// Package registry is the authoritative licence-tagged model registry. The
// registry — not a model file's own (forgeable, optional) metadata — is the
// source of truth (research/comfyui-model-identity-2026-05.md Q2). It is loaded
// from a GitOps-reviewable JSON config and keyed on filename (+ sha256 for
// integrity pinning).
package registry

import (
	"encoding/json"
	"fmt"
)

// Entry is one licence-tagged model.
type Entry struct {
	Filename     string   `json:"filename"`
	SHA256       string   `json:"sha256"`
	Licence      string   `json:"licence"`
	CommercialOK bool     `json:"commercial_ok"`
	TierFit      []string `json:"tier_fit,omitempty"`
	Substitutes  []string `json:"substitutes,omitempty"`
}

// Registry resolves a model filename to its licence entry.
type Registry struct {
	byFilename map[string]Entry
}

// Load parses a JSON array of entries into a Registry.
func Load(data []byte) (*Registry, error) {
	var entries []Entry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("load registry: %w", err)
	}
	byFilename := make(map[string]Entry, len(entries))
	for _, e := range entries {
		byFilename[e.Filename] = e
	}
	return &Registry{byFilename: byFilename}, nil
}

// Resolve returns the entry for a model filename and whether it is known.
func (r *Registry) Resolve(filename string) (Entry, bool) {
	e, ok := r.byFilename[filename]
	return e, ok
}
