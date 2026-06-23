// Package workflow parses a ComfyUI POST /prompt body into a node graph and,
// against ONE per-class node allowlist (default-deny), extracts model references
// (for the licence gate) and exposes output-saver fields (for per-user isolation).
//
// The allowlist is authoritative: a class_type absent from it is rejected before
// execution (Validate), which closes the custom-node smuggle path for BOTH model
// loaders and output savers (research/comfyui-model-identity-2026-05.md Q3 +
// image-generation-and-licensing.md §7, ADR 015). In production the allowlist is
// loaded from a CI-generated config (derived from the curated workflows); the
// hardcoded DefaultAllowlist here is the intrinsic ComfyUI node→field table used
// as the generator's source and as the test fixture.
package workflow

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
)

// embeddingRef matches ComfyUI inline textual-inversion references in prompt
// text: "embedding:name" or "embedding:name.safetensors" (extension optional).
var embeddingRef = regexp.MustCompile(`embedding:([A-Za-z0-9_./\-]+)`)

// Node is a single ComfyUI graph node.
type Node struct {
	ClassType string         `json:"class_type"`
	Inputs    map[string]any `json:"inputs"`
}

// Graph is the parsed prompt graph, keyed by node ID.
type Graph struct {
	Nodes map[string]Node
}

// ModelField names one model-name input on a loader node and the ComfyUI
// models/ subfolder the referenced file lives in.
type ModelField struct {
	Field  string `json:"field"`
	Folder string `json:"folder"`
}

// Spec is the per-class metadata for an allowlisted node. A node may load
// models (ModelFields, used by the licence gate), write a file (OutputField,
// used by per-user isolation), both (rare), or neither (most nodes — KSampler,
// EmptyLatentImage, … — still allowlisted, just no special handling).
type Spec struct {
	ModelFields []ModelField `json:"model_fields,omitempty"`
	OutputField string       `json:"output_field,omitempty"`
	// InputField names the image-input field a loader node reads (LoadImage's
	// "image"). It drives per-user input isolation the way OutputField drives
	// output bucketing: the gate scopes/validates this field's value to <user>/…
	InputField string `json:"input_field,omitempty"`
}

// Allowlist maps an allowed class_type to its per-class metadata.
type Allowlist map[string]Spec

// ModelRef is one model file referenced by one loader node field.
type ModelRef struct {
	NodeID    string
	ClassType string
	Field     string
	Filename  string
	Folder    string
}

// DefaultAllowlist is the verified intrinsic ComfyUI node→field table (loader
// fields from research/comfyui-model-identity-2026-05.md Q3; saver fields
// verified against ComfyUI v0.22.0 — see CONTRACT-NOTES.md). It also allowlists
// the no-special-handling nodes a minimal SDXL workshop workflow uses.
var DefaultAllowlist = Allowlist{
	// Model loaders (SafeTensors only; GGUF/pickle loaders deliberately excluded).
	"CheckpointLoaderSimple": {ModelFields: []ModelField{{"ckpt_name", "checkpoints"}}},
	"UNETLoader":             {ModelFields: []ModelField{{"unet_name", "diffusion_models"}}},
	"VAELoader":              {ModelFields: []ModelField{{"vae_name", "vae"}}},
	"LoraLoader":             {ModelFields: []ModelField{{"lora_name", "loras"}}},
	"CLIPLoader":             {ModelFields: []ModelField{{"clip_name", "text_encoders"}}},
	"DualCLIPLoader":         {ModelFields: []ModelField{{"clip_name1", "text_encoders"}, {"clip_name2", "text_encoders"}}},
	"ControlNetLoader":       {ModelFields: []ModelField{{"control_net_name", "controlnet"}}},
	// Outcome-modifier loaders — role-gating must see every asset that steers the
	// render, not just the checkpoint (plan 2026-06-05-role-tier-gating).
	"LoraLoaderModelOnly":  {ModelFields: []ModelField{{"lora_name", "loras"}}},
	"IPAdapterModelLoader": {ModelFields: []ModelField{{"ipadapter_file", "ipadapter"}}},
	"HypernetworkLoader":   {ModelFields: []ModelField{{"hypernetwork_name", "hypernetworks"}}},
	"StyleModelLoader":     {ModelFields: []ModelField{{"style_model_name", "style_models"}}},
	"GLIGENLoader":         {ModelFields: []ModelField{{"gligen_name", "gligen"}}},
	// Output savers (image; the only Phase-1 savers — audio/video/model-writers
	// are intentionally absent so they're rejected by default-deny).
	"SaveImage":        {OutputField: "filename_prefix"},
	"SaveAnimatedWEBP": {OutputField: "filename_prefix"},
	"SaveAnimatedPNG":  {OutputField: "filename_prefix"},
	// Image input loaders — InputField scopes the selected file to the caller's
	// own /input/<user>/ bucket (per-user upload isolation).
	"LoadImage":     {InputField: "image"},
	"LoadImageMask": {InputField: "image"},
	// No-special-handling nodes used by a standard SDXL text2img workshop graph.
	"CLIPTextEncode":   {},
	"KSampler":         {},
	"EmptyLatentImage": {},
	"VAEDecode":        {},
}

// promptBody is the POST /prompt envelope; the graph lives under "prompt".
type promptBody struct {
	Prompt map[string]Node `json:"prompt"`
}

// Parse reads a ComfyUI POST /prompt body into a Graph.
func Parse(body []byte) (*Graph, error) {
	var pb promptBody
	if err := json.Unmarshal(body, &pb); err != nil {
		return nil, fmt.Errorf("parse prompt body: %w", err)
	}
	if pb.Prompt == nil {
		return nil, fmt.Errorf("parse prompt body: missing \"prompt\" object")
	}
	return &Graph{Nodes: pb.Prompt}, nil
}

// LoadAllowlist parses a JSON object (class_type → Spec) into an Allowlist —
// the production path, consuming the CI-generated allowlist config.
func LoadAllowlist(data []byte) (Allowlist, error) {
	var a Allowlist
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, fmt.Errorf("load allowlist: %w", err)
	}
	return a, nil
}

// sortedIDs returns the node IDs in deterministic order.
func (g *Graph) sortedIDs() []string {
	ids := make([]string, 0, len(g.Nodes))
	for id := range g.Nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Validate rejects the whole workflow if any node's class_type is not on the
// allowlist (default-deny). This is the single check that closes both the
// loader-smuggle and saver-escape blind spots.
func (g *Graph) Validate(allow Allowlist) error {
	for _, id := range g.sortedIDs() {
		ct := g.Nodes[id].ClassType
		if _, ok := allow[ct]; !ok {
			return fmt.Errorf("node %s: class_type %q is not allowlisted", id, ct)
		}
	}
	return nil
}

// ModelReferences returns every model file referenced by an allowlisted loader
// node, in deterministic node-ID order. A non-allowlisted class_type yields no
// references (Validate rejects it separately). A missing or non-string field
// value is skipped.
func (g *Graph) ModelReferences(allow Allowlist) []ModelRef {
	var refs []ModelRef
	for _, id := range g.sortedIDs() {
		node := g.Nodes[id]
		spec, ok := allow[node.ClassType]
		if !ok {
			continue
		}
		for _, mf := range spec.ModelFields {
			v, ok := node.Inputs[mf.Field].(string)
			if !ok {
				continue
			}
			refs = append(refs, ModelRef{
				NodeID:    id,
				ClassType: node.ClassType,
				Field:     mf.Field,
				Filename:  v,
				Folder:    mf.Folder,
			})
		}
	}
	// Inline textual-inversion embeddings live in CLIPTextEncode text, not a
	// loader field — scan them so they role-gate like any other asset
	// (plan 2026-06-05-role-tier-gating).
	for _, id := range g.sortedIDs() {
		node := g.Nodes[id]
		if node.ClassType != "CLIPTextEncode" {
			continue
		}
		text, _ := node.Inputs["text"].(string)
		for _, m := range embeddingRef.FindAllStringSubmatch(text, -1) {
			refs = append(refs, ModelRef{
				NodeID:    id,
				ClassType: node.ClassType,
				Field:     "text",
				Filename:  m[1],
				Folder:    "embeddings",
			})
		}
	}
	return refs
}
