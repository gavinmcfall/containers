package tier

import (
	"testing"

	"github.com/gavinmcfall/containers/apps/licence-gate/internal/registry"
	"github.com/gavinmcfall/containers/apps/licence-gate/internal/workflow"
)

func mustReg(t *testing.T, j string) *registry.Registry {
	t.Helper()
	r, err := registry.Load([]byte(j))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestFilterDropsIncapableWorkers(t *testing.T) {
	reg := mustReg(t, `[{"filename":"sdxl.safetensors","commercial_ok":true,"tier_fit":["heavy"]}]`)
	workers := Map{"venge": "heavy", "vixen": "heavy", "nova": "light"}
	refs := []workflow.ModelRef{{Filename: "sdxl.safetensors"}}

	got, err := Filter(refs, reg, workers, []string{"venge", "vixen", "nova"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(got) != 2 || got[0] != "venge" || got[1] != "vixen" {
		t.Errorf("filtered = %v, want [venge vixen] (nova dropped, order preserved)", got)
	}
}

func TestFilterKeepsAllForSD15(t *testing.T) {
	reg := mustReg(t, `[{"filename":"sd15.safetensors","commercial_ok":true,"tier_fit":["heavy","light"]}]`)
	workers := Map{"venge": "heavy", "nova": "light"}
	got, err := Filter([]workflow.ModelRef{{Filename: "sd15.safetensors"}}, reg, workers, []string{"venge", "nova"})
	if err != nil || len(got) != 2 {
		t.Fatalf("got=%v err=%v, want both kept", got, err)
	}
}

func TestFilterErrorsWhenNoneQualify(t *testing.T) {
	reg := mustReg(t, `[{"filename":"sdxl.safetensors","commercial_ok":true,"tier_fit":["heavy"]}]`)
	workers := Map{"nova": "light", "blaze": "light"}
	_, err := Filter([]workflow.ModelRef{{Filename: "sdxl.safetensors"}}, reg, workers, []string{"nova", "blaze"})
	if err == nil {
		t.Fatal("want error when no worker qualifies")
	}
}

func TestFilterUnknownModelIsPermissive(t *testing.T) {
	reg := mustReg(t, `[]`)
	workers := Map{"venge": "heavy", "nova": "light"}
	got, err := Filter([]workflow.ModelRef{{Filename: "x.safetensors"}}, reg, workers, []string{"venge", "nova"})
	if err != nil || len(got) != 2 {
		t.Fatalf("unknown should not constrain: got=%v err=%v", got, err)
	}
}

func TestFilterMultiModelIntersection(t *testing.T) {
	// an SD1.5 base (heavy+light) plus a heavy-only LoRA => job is heavy-only.
	reg := mustReg(t, `[
	  {"filename":"sd15.safetensors","commercial_ok":true,"tier_fit":["heavy","light"]},
	  {"filename":"big_lora.safetensors","commercial_ok":true,"tier_fit":["heavy"]}
	]`)
	workers := Map{"venge": "heavy", "nova": "light"}
	refs := []workflow.ModelRef{{Filename: "sd15.safetensors"}, {Filename: "big_lora.safetensors"}}
	got, err := Filter(refs, reg, workers, []string{"venge", "nova"})
	if err != nil || len(got) != 1 || got[0] != "venge" {
		t.Fatalf("intersection should be heavy-only: got=%v err=%v", got, err)
	}
}

func TestLoadFromGPUConfig(t *testing.T) {
	m, err := LoadFromGPUConfig([]byte(`{"workers":[
	  {"id":"venge","name":"Vengeance","tier":"heavy"},
	  {"id":"nova","name":"Nova","tier":"light"},
	  {"id":"untagged","name":"Old"}
	]}`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if m["venge"] != "heavy" || m["nova"] != "light" {
		t.Errorf("tiers wrong: %v", m)
	}
	if m["untagged"] != "" {
		t.Errorf("untagged worker should map to empty tier, got %q", m["untagged"])
	}
}
