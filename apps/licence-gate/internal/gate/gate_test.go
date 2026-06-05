package gate

import (
	"testing"

	"github.com/gavinmcfall/containers/apps/licence-gate/internal/registry"
	"github.com/gavinmcfall/containers/apps/licence-gate/internal/workflow"
)

func testRegistry(t *testing.T) *registry.Registry {
	t.Helper()
	r, err := registry.Load([]byte(`[
		{"filename":"sd_xl_base_1.0.safetensors","licence":"OpenRAIL++-M","commercial_ok":true},
		{"filename":"flux1-dev-fp8.safetensors","licence":"FLUX.1 dev NC","commercial_ok":false,"substitutes":["flux1-schnell-fp8.safetensors"]}
	]`))
	if err != nil {
		t.Fatalf("registry load: %v", err)
	}
	return r
}

// The North Star "won't accept": a commercial job that references a
// non-commercial model is rejected, and commercial-safe substitutes are offered.
func TestCommercialJobWithNonCommercialModelIsRejected(t *testing.T) {
	refs := []workflow.ModelRef{
		{NodeID: "4", ClassType: "UNETLoader", Field: "unet_name", Filename: "flux1-dev-fp8.safetensors", Folder: "diffusion_models"},
	}

	d := Evaluate(refs, testRegistry(t), Job{Commercial: true})

	if d.Allowed {
		t.Fatal("commercial job on a non-commercial model must NOT be allowed")
	}
	if d.Action != ActionReject {
		t.Errorf("action = %q, want reject", d.Action)
	}
	if len(d.Violations) != 1 || d.Violations[0].Filename != "flux1-dev-fp8.safetensors" {
		t.Fatalf("violations = %+v", d.Violations)
	}
	v := d.Violations[0]
	if v.Reason != ReasonNonCommercial {
		t.Errorf("reason = %q, want non-commercial", v.Reason)
	}
	if len(v.Substitutes) != 1 || v.Substitutes[0] != "flux1-schnell-fp8.safetensors" {
		t.Errorf("want commercial-safe substitute offered, got %v", v.Substitutes)
	}
}

// A commercial job using a commercial-OK model is allowed.
func TestCommercialJobWithCommercialModelIsAllowed(t *testing.T) {
	refs := []workflow.ModelRef{
		{NodeID: "4", ClassType: "CheckpointLoaderSimple", Field: "ckpt_name", Filename: "sd_xl_base_1.0.safetensors", Folder: "checkpoints"},
	}
	d := Evaluate(refs, testRegistry(t), Job{Commercial: true})
	if !d.Allowed || d.Action != ActionAllow {
		t.Fatalf("commercial job on commercial-OK model should be allowed: %+v", d)
	}
}

// Commercial-by-default conservatism: an unknown model (not in the registry)
// is blocked for a commercial job (unknown licence = unsafe).
func TestUnknownModelBlockedForCommercialJob(t *testing.T) {
	refs := []workflow.ModelRef{
		{NodeID: "4", ClassType: "CheckpointLoaderSimple", Field: "ckpt_name", Filename: "mystery.safetensors", Folder: "checkpoints"},
	}
	d := Evaluate(refs, testRegistry(t), Job{Commercial: true})
	if d.Allowed {
		t.Fatal("unknown model must be blocked for a commercial job")
	}
	if d.Violations[0].Reason != ReasonUnknown {
		t.Errorf("reason = %q, want unknown", d.Violations[0].Reason)
	}
}

// A personal job may use a non-commercial model (the personal flag unlocks them).
func TestPersonalJobMayUseNonCommercialModel(t *testing.T) {
	refs := []workflow.ModelRef{
		{NodeID: "4", ClassType: "UNETLoader", Field: "unet_name", Filename: "flux1-dev-fp8.safetensors", Folder: "diffusion_models"},
	}
	d := Evaluate(refs, testRegistry(t), Job{Commercial: false})
	if !d.Allowed {
		t.Fatalf("personal job should be allowed to use a non-commercial model: %+v", d)
	}
}

func TestEvaluateRejectsMissingGroup(t *testing.T) {
	reg, _ := registry.Load([]byte(`[
	  {"filename":"nsfw.safetensors","commercial_ok":true,"requires_group":"mature-content"}
	]`))
	refs := []workflow.ModelRef{{Filename: "nsfw.safetensors"}}

	// caller WITHOUT the group, personal job (licence fine) -> reject on group
	d := Evaluate(refs, reg, Job{Commercial: false, Groups: []string{"family-minor"}})
	if d.Allowed || len(d.Violations) != 1 || d.Violations[0].Reason != ReasonRequiresGroup {
		t.Fatalf("missing-group: %+v, want reject requires-group", d)
	}
	// caller WITH the group -> allow
	d2 := Evaluate(refs, reg, Job{Commercial: false, Groups: []string{"mature-content"}})
	if !d2.Allowed {
		t.Fatalf("with-group should allow: %+v", d2)
	}
	// untagged model -> allow regardless of groups
	reg2, _ := registry.Load([]byte(`[{"filename":"open.safetensors","commercial_ok":true}]`))
	d3 := Evaluate([]workflow.ModelRef{{Filename: "open.safetensors"}}, reg2, Job{Commercial: false, Groups: nil})
	if !d3.Allowed {
		t.Fatalf("untagged should allow: %+v", d3)
	}
}
