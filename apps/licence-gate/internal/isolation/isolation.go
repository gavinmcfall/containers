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
