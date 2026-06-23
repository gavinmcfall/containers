package isolation

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gavinmcfall/containers/apps/licence-gate/internal/workflow"
)

// inputImageExts mirrors ComfyUI's LoadImage content-type filter (image/*).
var inputImageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".webp": true,
	".gif": true, ".bmp": true, ".avif": true,
}

// ListUserInputs returns the image files in the caller's own input bucket
// (root/<user>/) as bare filenames, sorted. A missing bucket yields an empty
// list (the user simply has no uploads yet), never an error. Non-image files and
// sub-directories are skipped, matching ComfyUI's LoadImage content-type filter.
func ListUserInputs(root, user string) ([]string, error) {
	if root == "" || !safeUserSegment(user) {
		return nil, nil
	}
	entries, err := os.ReadDir(filepath.Join(root, user))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if inputImageExts[strings.ToLower(filepath.Ext(e.Name()))] {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	return files, nil
}

// safeUserSegment guards the user id used as a path segment (defence in depth —
// the id comes from verified identity, but it must never traverse).
func safeUserSegment(s string) bool {
	return s != "" && !strings.ContainsAny(s, `/\`) && !strings.Contains(s, "..")
}

// InjectObjectInfo rewrites a ComfyUI /object_info response so every allowlisted
// image-loader class (InputField set) lists only the caller's own uploads
// (<user>/file), replacing ComfyUI's global top-level listing. The combo options
// object (index 1) and EVERY other class are preserved byte-for-byte: only the
// target classes are re-encoded (with UseNumber, so no numeric default is lost to
// float64), so large integer maxes elsewhere survive intact. Works on both the
// full map and a single-node (/object_info/{class}) response. A class with an
// unexpected shape is left untouched (defensive).
func InjectObjectInfo(body []byte, user string, files []string, allow workflow.Allowlist) ([]byte, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, err
	}
	opts := make([]any, 0, len(files))
	for _, f := range files {
		opts = append(opts, user+"/"+f)
	}
	for class, spec := range allow {
		if spec.InputField == "" {
			continue
		}
		raw, ok := top[class]
		if !ok {
			continue
		}
		patched, changed := patchClassImageList(raw, spec.InputField, opts)
		if changed {
			top[class] = patched
		}
	}
	return json.Marshal(top)
}

// patchClassImageList replaces input.required.<field>[0] with opts in a single
// class's object_info, preserving index 1 (and everything else). It decodes with
// UseNumber so numeric defaults round-trip exactly. Returns the original raw and
// changed=false if the shape is not what we expect.
func patchClassImageList(raw json.RawMessage, field string, opts []any) (json.RawMessage, bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var node map[string]any
	if err := dec.Decode(&node); err != nil {
		return raw, false
	}
	input, ok := node["input"].(map[string]any)
	if !ok {
		return raw, false
	}
	required, ok := input["required"].(map[string]any)
	if !ok {
		return raw, false
	}
	entry, ok := required[field].([]any)
	if !ok || len(entry) == 0 {
		return raw, false
	}
	newEntry := make([]any, len(entry))
	copy(newEntry, entry)
	newEntry[0] = append([]any{}, opts...)
	required[field] = newEntry
	out, err := json.Marshal(node)
	if err != nil {
		return raw, false
	}
	return out, true
}
