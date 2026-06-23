package isolation

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gavinmcfall/containers/apps/licence-gate/internal/workflow"
)

func TestListUserInputsReturnsOnlyOwnImages(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "alice", "cat.png"))
	mustWrite(t, filepath.Join(root, "alice", "dog.jpg"))
	mustWrite(t, filepath.Join(root, "alice", "notes.txt"))
	mustWrite(t, filepath.Join(root, "bob", "secret.png"))

	got, err := ListUserInputs(root, "alice")
	if err != nil {
		t.Fatalf("ListUserInputs: %v", err)
	}
	want := []string{"cat.png", "dog.jpg"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %v, want %v (no notes.txt, no bob)", got, want)
	}
}

func TestListUserInputsMissingBucketIsEmpty(t *testing.T) {
	got, err := ListUserInputs(t.TempDir(), "nobody")
	if err != nil || len(got) != 0 {
		t.Fatalf("missing bucket should be empty,nil; got %v,%v", got, err)
	}
}

func TestInjectObjectInfoReplacesLoadImageList(t *testing.T) {
	body := []byte(`{
		"LoadImage":{"input":{"required":{"image":[["example.png"],{"image_upload":true}]}}},
		"KSampler":{"input":{"required":{"seed":[["INT"],{"max":18446744073709551615}]}}}
	}`)
	out, err := InjectObjectInfo(body, "alice", []string{"cat.png", "dog.jpg"}, workflow.DefaultAllowlist)
	if err != nil {
		t.Fatalf("InjectObjectInfo: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("result not valid json: %v", err)
	}
	img := m["LoadImage"].(map[string]any)["input"].(map[string]any)["required"].(map[string]any)["image"].([]any)
	list := img[0].([]any)
	if len(list) != 2 || list[0] != "alice/cat.png" || list[1] != "alice/dog.jpg" {
		t.Fatalf("image list = %v, want [alice/cat.png alice/dog.jpg]", list)
	}
	if opts, ok := img[1].(map[string]any); !ok || opts["image_upload"] != true {
		t.Fatalf("image_upload option must be preserved, got %v", img[1])
	}
	// The huge seed max in an untouched class must survive byte-exact (no float64).
	if !strings.Contains(string(out), "18446744073709551615") {
		t.Fatalf("untouched KSampler seed max was corrupted: %s", out)
	}
}

func TestInjectObjectInfoLeavesMalformedAlone(t *testing.T) {
	body := []byte(`{"LoadImage":{"input":{"required":{}}}}`)
	out, err := InjectObjectInfo(body, "alice", []string{"cat.png"}, workflow.DefaultAllowlist)
	if err != nil {
		t.Fatalf("should not error on odd shape: %v", err)
	}
	if !strings.Contains(string(out), "LoadImage") {
		t.Fatalf("body should be returned intact: %s", out)
	}
}

func mustWrite(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}
