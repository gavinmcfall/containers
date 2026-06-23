// Package isolation enforces per-user output bucketing in the licence-gate proxy
// (ADR 015). Write side: rewrite the filename field of every allowlisted output
// saver to land under the caller's bucket (<user>/…). Saver identity + field
// name come from the single workflow.Allowlist (Spec.OutputField); every other
// output-emitting node (audio/video/latent/model-writers) is absent from the
// allowlist and rejected by workflow.Validate before exec (default-deny; see
// CONTRACT-NOTES.md).
package isolation

import (
	"fmt"
	"path"
	"strings"

	"github.com/gavinmcfall/containers/apps/licence-gate/internal/workflow"
)

// defaultPrefix is ComfyUI's built-in filename_prefix default.
const defaultPrefix = "ComfyUI"

// unsafePrefix reports whether a prefix could escape the user's bucket.
func unsafePrefix(p string) bool {
	return strings.ContainsAny(p, `/\`) || strings.Contains(p, "..") || strings.Contains(p, "://")
}

// RewriteOutputs scopes every allowlisted saver's output field under user/.
// An absent or empty prefix takes ComfyUI's default ("ComfyUI") before scoping.
// A prefix that could escape the bucket (separator, traversal, scheme) rejects
// the whole workflow.
func RewriteOutputs(g *workflow.Graph, user string, allow workflow.Allowlist) error {
	for id, node := range g.Nodes {
		spec, ok := allow[node.ClassType]
		if !ok || spec.OutputField == "" {
			continue
		}
		field := spec.OutputField
		prefix, _ := node.Inputs[field].(string)
		if prefix == "" {
			prefix = defaultPrefix
		}
		if unsafePrefix(prefix) {
			return fmt.Errorf("node %s (%s): unsafe %s %q", id, node.ClassType, field, prefix)
		}
		// node.Inputs is a map (reference) — mutation is reflected in g.Nodes[id].
		node.Inputs[field] = user + "/" + prefix
	}
	return nil
}

// ScopeInputs is the per-user upload security boundary: every allowlisted image
// loader's InputField value in a submitted graph must reference a file in the
// caller's OWN bucket (first path segment == user). An empty/absent value (no
// image selected) or a non-string value (a graph-link input, not a file) is left
// alone; any other-user reference, bare top-level filename, or traversal/escape
// rejects the whole workflow. This is enforcement; the object_info dropdown
// filtering is only UX and a hand-edited graph would bypass it.
func ScopeInputs(g *workflow.Graph, user string, allow workflow.Allowlist) error {
	for id, node := range g.Nodes {
		spec, ok := allow[node.ClassType]
		if !ok || spec.InputField == "" {
			continue
		}
		val, isStr := node.Inputs[spec.InputField].(string)
		if !isStr || val == "" {
			continue
		}
		if err := inputInUserBucket(user, val); err != nil {
			return fmt.Errorf("node %s (%s): %w", id, node.ClassType, err)
		}
	}
	return nil
}

// inputInUserBucket reports whether val is a safe <user>/… input reference.
func inputInUserBucket(user, val string) error {
	if strings.ContainsAny(val, `\`) || strings.Contains(val, "..") ||
		strings.Contains(val, "://") || strings.HasPrefix(val, "/") {
		return fmt.Errorf("unsafe input path %q", val)
	}
	rel := path.Clean(val)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return fmt.Errorf("input path escapes the input store: %q", val)
	}
	first := rel
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		first = rel[:i]
	}
	if first != user {
		return fmt.Errorf("input %q is not in %q's bucket", val, user)
	}
	return nil
}
