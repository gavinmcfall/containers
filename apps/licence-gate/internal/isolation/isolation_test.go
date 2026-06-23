package isolation

import (
	"testing"

	"github.com/gavinmcfall/containers/apps/licence-gate/internal/workflow"
)

func parse(t *testing.T, body string) *workflow.Graph {
	t.Helper()
	g, err := workflow.Parse([]byte(body))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return g
}

// The saver field name per class_type is EXPLICIT in the unified allowlist
// (verified against ComfyUI v0.22.0), never an assumed convention — same
// discipline as the loader-node table.
func TestSaverAllowlistFieldsAreExplicit(t *testing.T) {
	for _, ct := range []string{"SaveImage", "SaveAnimatedWEBP", "SaveAnimatedPNG"} {
		if workflow.DefaultAllowlist[ct].OutputField != "filename_prefix" {
			t.Errorf("%s should map to filename_prefix, got %q", ct, workflow.DefaultAllowlist[ct].OutputField)
		}
	}
}

func TestRewriteScopesSaverToUserBucket(t *testing.T) {
	g := parse(t, `{"prompt":{"9":{"class_type":"SaveImage","inputs":{"filename_prefix":"ComfyUI"}}}}`)
	if err := RewriteOutputs(g, "alice", workflow.DefaultAllowlist); err != nil {
		t.Fatalf("RewriteOutputs: %v", err)
	}
	if got := g.Nodes["9"].Inputs["filename_prefix"]; got != "alice/ComfyUI" {
		t.Errorf("filename_prefix = %v, want alice/ComfyUI", got)
	}
}

// Every allowlisted saver in the graph is rewritten, not just the first.
func TestRewriteAllSaversInOneGraph(t *testing.T) {
	g := parse(t, `{"prompt":{
		"9":{"class_type":"SaveImage","inputs":{"filename_prefix":"a"}},
		"10":{"class_type":"SaveAnimatedWEBP","inputs":{"filename_prefix":"b"}}
	}}`)
	if err := RewriteOutputs(g, "alice", workflow.DefaultAllowlist); err != nil {
		t.Fatalf("RewriteOutputs: %v", err)
	}
	if g.Nodes["9"].Inputs["filename_prefix"] != "alice/a" || g.Nodes["10"].Inputs["filename_prefix"] != "alice/b" {
		t.Errorf("both savers must be rewritten: %v / %v",
			g.Nodes["9"].Inputs["filename_prefix"], g.Nodes["10"].Inputs["filename_prefix"])
	}
}

// ComfyUI's SaveImage default filename_prefix is "ComfyUI"; an absent or empty
// prefix must become "<user>/ComfyUI", never an error.
func TestRewriteDefaultsMissingPrefix(t *testing.T) {
	for _, body := range []string{
		`{"prompt":{"9":{"class_type":"SaveImage","inputs":{}}}}`,
		`{"prompt":{"9":{"class_type":"SaveImage","inputs":{"filename_prefix":""}}}}`,
	} {
		g := parse(t, body)
		if err := RewriteOutputs(g, "alice", workflow.DefaultAllowlist); err != nil {
			t.Fatalf("RewriteOutputs(%s): %v", body, err)
		}
		if got := g.Nodes["9"].Inputs["filename_prefix"]; got != "alice/ComfyUI" {
			t.Errorf("missing/empty prefix should default: got %v", got)
		}
	}
}

// A prefix carrying a path separator, traversal, or scheme could escape the
// user's bucket — reject the whole workflow before exec. The graph is built
// directly so backslash/scheme values aren't mangled by JSON string escaping.
func TestRewriteRejectsUnsafePrefix(t *testing.T) {
	for _, bad := range []string{"sub/dir", "../bob", "..", "http://evil/x", `back\slash`} {
		g := &workflow.Graph{Nodes: map[string]workflow.Node{
			"9": {ClassType: "SaveImage", Inputs: map[string]any{"filename_prefix": bad}},
		}}
		if err := RewriteOutputs(g, "alice", workflow.DefaultAllowlist); err == nil {
			t.Errorf("unsafe prefix %q should be rejected", bad)
		}
	}
}

// ScopeInputs is the real per-user security boundary for uploads: a submitted
// /prompt graph may only reference input files in the caller's own bucket
// (<user>/…). The object_info dropdown filtering is UX; this stops a hand-edited
// graph from loading another member's upload. Empty/absent value = no image = ok.
func TestScopeInputsAllowsOwnInput(t *testing.T) {
	g := parse(t, `{"prompt":{"1":{"class_type":"LoadImage","inputs":{"image":"alice/cat.png"}}}}`)
	if err := ScopeInputs(g, "alice", workflow.DefaultAllowlist); err != nil {
		t.Fatalf("own input should be allowed: %v", err)
	}
	if got := g.Nodes["1"].Inputs["image"]; got != "alice/cat.png" {
		t.Errorf("value must be left intact, got %v", got)
	}
}

func TestScopeInputsDeniesOtherUsersInput(t *testing.T) {
	g := parse(t, `{"prompt":{"1":{"class_type":"LoadImage","inputs":{"image":"bob/cat.png"}}}}`)
	if err := ScopeInputs(g, "alice", workflow.DefaultAllowlist); err == nil {
		t.Fatal("referencing another user's input must be rejected")
	}
}

func TestScopeInputsDeniesBareFilename(t *testing.T) {
	g := parse(t, `{"prompt":{"1":{"class_type":"LoadImage","inputs":{"image":"example.png"}}}}`)
	if err := ScopeInputs(g, "alice", workflow.DefaultAllowlist); err == nil {
		t.Fatal("bare top-level input (no user subfolder) must be rejected")
	}
}

func TestScopeInputsAllowsEmptyOrAbsent(t *testing.T) {
	for _, body := range []string{
		`{"prompt":{"1":{"class_type":"LoadImage","inputs":{"image":""}}}}`,
		`{"prompt":{"1":{"class_type":"LoadImage","inputs":{}}}}`,
	} {
		g := parse(t, body)
		if err := ScopeInputs(g, "alice", workflow.DefaultAllowlist); err != nil {
			t.Errorf("empty/absent image should be allowed (%s): %v", body, err)
		}
	}
}

func TestScopeInputsRejectsTraversal(t *testing.T) {
	// `alice\\x.png` is the JSON encoding of the literal path alice\x.png.
	for _, val := range []string{"alice/../bob/x.png", "../x.png", `alice\\x.png`, "/etc/passwd", "http://x/y.png"} {
		g := parse(t, `{"prompt":{"1":{"class_type":"LoadImage","inputs":{"image":"`+val+`"}}}}`)
		if err := ScopeInputs(g, "alice", workflow.DefaultAllowlist); err == nil {
			t.Errorf("traversal/escape %q must be rejected", val)
		}
	}
}
