package workflow

import "testing"

// A ComfyUI POST /prompt body wraps the node graph under "prompt".
// The parser must extract model references from allowlisted loader nodes,
// enumerating every model-name field per node (the research doc's table).
func TestModelReferencesExtractsCheckpointLoader(t *testing.T) {
	body := []byte(`{
		"client_id": "abc",
		"prompt": {
			"4": {
				"class_type": "CheckpointLoaderSimple",
				"inputs": {"ckpt_name": "sd_xl_base_1.0.safetensors"}
			}
		}
	}`)

	g, err := Parse(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	refs := g.ModelReferences(DefaultAllowlist)
	if len(refs) != 1 {
		t.Fatalf("want 1 model ref, got %d: %+v", len(refs), refs)
	}
	got := refs[0]
	if got.NodeID != "4" || got.ClassType != "CheckpointLoaderSimple" ||
		got.Field != "ckpt_name" || got.Filename != "sd_xl_base_1.0.safetensors" ||
		got.Folder != "checkpoints" {
		t.Fatalf("unexpected ref: %+v", got)
	}
}

// Default-deny: a workflow containing any non-allowlisted class_type is rejected
// before execution — the single check that closes the loader-smuggle AND
// saver-escape blind spots.
func TestValidateRejectsUnknownClassType(t *testing.T) {
	g, err := Parse([]byte(`{"prompt":{
		"4": {"class_type": "CheckpointLoaderSimple", "inputs": {"ckpt_name": "ok.safetensors"}},
		"5": {"class_type": "EvilCustomLoaderNode", "inputs": {}}
	}}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := g.Validate(DefaultAllowlist); err == nil {
		t.Fatal("workflow with a non-allowlisted class_type must be rejected")
	}
}

// The ComfyUI model-writer save nodes (CheckpointSave, LoraSave, …) are a real
// escape vector (they write model files). They are NOT on the allowlist, so
// Validate rejects them — defense-in-depth alongside the RO model store.
func TestValidateRejectsModelWriterEscapeVector(t *testing.T) {
	for _, ct := range []string{"CheckpointSave", "LoraSave", "ModelSave", "VAESave", "SaveImageWebsocket"} {
		g := &Graph{Nodes: map[string]Node{"1": {ClassType: ct, Inputs: map[string]any{}}}}
		if err := g.Validate(DefaultAllowlist); err == nil {
			t.Errorf("%s must be rejected by default-deny", ct)
		}
	}
}

// A standard SDXL text2img workshop graph uses only allowlisted nodes → passes.
func TestValidateAllowsStandardWorkshopGraph(t *testing.T) {
	g, err := Parse([]byte(`{"prompt":{
		"4": {"class_type": "CheckpointLoaderSimple", "inputs": {"ckpt_name": "sdxl.safetensors"}},
		"5": {"class_type": "CLIPTextEncode", "inputs": {"text": "a cat"}},
		"6": {"class_type": "CLIPTextEncode", "inputs": {"text": "blurry"}},
		"7": {"class_type": "EmptyLatentImage", "inputs": {"width": 1024, "height": 1024}},
		"8": {"class_type": "KSampler", "inputs": {"seed": 1}},
		"9": {"class_type": "VAEDecode", "inputs": {}},
		"10": {"class_type": "SaveImage", "inputs": {"filename_prefix": "out"}}
	}}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := g.Validate(DefaultAllowlist); err != nil {
		t.Fatalf("standard workshop graph should validate: %v", err)
	}
}

func TestModelReferencesCoversOutcomeModifiers(t *testing.T) {
	body := []byte(`{"prompt":{
	  "1":{"class_type":"LoraLoaderModelOnly","inputs":{"lora_name":"nsfw_lora.safetensors"}},
	  "2":{"class_type":"IPAdapterModelLoader","inputs":{"ipadapter_file":"ip.safetensors"}},
	  "3":{"class_type":"HypernetworkLoader","inputs":{"hypernetwork_name":"h.pt"}},
	  "4":{"class_type":"StyleModelLoader","inputs":{"style_model_name":"s.safetensors"}},
	  "5":{"class_type":"GLIGENLoader","inputs":{"gligen_name":"g.safetensors"}}
	}}`)
	g, err := Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, r := range g.ModelReferences(DefaultAllowlist) {
		got[r.Filename] = true
	}
	for _, want := range []string{"nsfw_lora.safetensors", "ip.safetensors", "h.pt", "s.safetensors", "g.safetensors"} {
		if !got[want] {
			t.Errorf("ref %q not extracted (loader not allowlisted)", want)
		}
	}
}

func TestModelReferencesExtractsEmbeddings(t *testing.T) {
	body := []byte(`{"prompt":{
	  "1":{"class_type":"CLIPTextEncode","inputs":{"text":"a photo, embedding:nsfw_ti, masterpiece embedding:another"}},
	  "2":{"class_type":"CLIPTextEncode","inputs":{"text":"no embeds here"}}
	}}`)
	g, _ := Parse(body)
	got := map[string]string{}
	for _, r := range g.ModelReferences(DefaultAllowlist) {
		got[r.Filename] = r.Folder
	}
	for _, name := range []string{"nsfw_ti", "another"} {
		if got[name] != "embeddings" {
			t.Errorf("embedding %q folder=%q, want embeddings (not extracted?)", name, got[name])
		}
	}
}

// InputField on a Spec identifies the image-input field a loader node reads
// (LoadImage's "image"), so per-user input isolation can scope/validate it the
// same way OutputField drives output bucketing. It must round-trip through the
// JSON allowlist loader and be present on the built-in default for LoadImage.
func TestInputFieldRoundTripsThroughAllowlistLoad(t *testing.T) {
	a, err := LoadAllowlist([]byte(`{"LoadImage":{"input_field":"image"}}`))
	if err != nil {
		t.Fatalf("LoadAllowlist: %v", err)
	}
	if got := a["LoadImage"].InputField; got != "image" {
		t.Fatalf("loaded LoadImage.InputField = %q, want %q", got, "image")
	}
}

func TestDefaultAllowlistTagsLoadImageInputField(t *testing.T) {
	if got := DefaultAllowlist["LoadImage"].InputField; got != "image" {
		t.Fatalf("DefaultAllowlist LoadImage.InputField = %q, want %q", got, "image")
	}
}
