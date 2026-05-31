package registry

import "testing"

// The registry is the authoritative licence source (NOT the model file's own
// metadata). It loads from a GitOps-reviewable JSON config and resolves a
// referenced filename to its licence entry. A filename not in the registry is
// unknown — the gate blocks unknown models for commercial jobs elsewhere.
const sample = `[
  {"filename": "sd_xl_base_1.0.safetensors", "sha256": "abc123", "licence": "CreativeML OpenRAIL++-M", "commercial_ok": true, "tier_fit": ["heavy","light"]},
  {"filename": "flux1-dev-fp8.safetensors", "sha256": "def456", "licence": "FLUX.1 [dev] Non-Commercial", "commercial_ok": false, "substitutes": ["flux1-schnell-fp8.safetensors"]}
]`

func TestResolveKnownModel(t *testing.T) {
	r, err := Load([]byte(sample))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	e, ok := r.Resolve("flux1-dev-fp8.safetensors")
	if !ok {
		t.Fatal("want flux1-dev-fp8 resolved, got not found")
	}
	if e.CommercialOK {
		t.Errorf("flux dev is non-commercial; CommercialOK should be false")
	}
	if e.Licence != "FLUX.1 [dev] Non-Commercial" {
		t.Errorf("licence = %q", e.Licence)
	}
	if len(e.Substitutes) != 1 || e.Substitutes[0] != "flux1-schnell-fp8.safetensors" {
		t.Errorf("substitutes = %v", e.Substitutes)
	}
}

func TestResolveUnknownModelNotFound(t *testing.T) {
	r, err := Load([]byte(sample))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := r.Resolve("some-random-model.safetensors"); ok {
		t.Fatal("unknown model should not resolve")
	}
}
