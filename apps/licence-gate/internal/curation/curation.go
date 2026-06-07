// Package curation surfaces a small, GitOps-managed set of vetted "curated"
// ComfyUI workflows into each family member's App Mode, role-scoped — the
// delivery mechanism the app-mode-curation design (sub-design #10) left as
// "out of scope" for the UI layer.
//
// ComfyUI has no multi-user concept; the licence-gate already scopes /userdata
// to per-user buckets (each caller has a private workflows/ namespace). Curated
// workflows live in NO user's bucket — they are overlaid by the proxy: merged
// (role-filtered) into the /userdata listing the browser fetches, and served
// from this Set on read. The curated namespace is read-only to the family.
//
// role_allowlist is the VISIBILITY layer per ADR 019 — it controls what appears
// in a caller's App Mode surface. It is NOT the security boundary: a model the
// caller may not use is still refused by the gate's model-tag check regardless of
// which workflow referenced it. Empty/absent role_allowlist = visible to all
// authenticated callers (e.g. the default SDXL base graph).
package curation

import (
	"strings"

	"github.com/gavinmcfall/containers/apps/licence-gate/internal/identity"
)

// Entry is one curated workflow.
type Entry struct {
	// Path is the logical userdata path from the user root, using "/" separators,
	// no leading slash — e.g. "workflows/lighthouse/portrait.json". The
	// "workflows/" root makes it appear in the App Mode workflow sidebar; a
	// "lighthouse/" subdir groups curated graphs and avoids collisions with a
	// caller's own top-level workflows.
	Path string
	// RoleAllowlist lists the claims that may see this entry. Plain entries match a
	// Pocket-ID group claim (family-adult, family-minor, mature-content); a
	// "brand:<id>" entry matches a brand identity with that user. Empty/nil =>
	// visible to every authenticated caller.
	RoleAllowlist []string
	// Content is the workflow JSON served on read.
	Content []byte
	// Size is the byte length reported in the listing (ComfyUI FileInfo.size).
	Size int
	// ModifiedMS is the mtime reported in the listing, milliseconds since epoch
	// (ComfyUI FileInfo.modified).
	ModifiedMS int64
}

// Set is the loaded curated-workflow collection.
type Set struct {
	entries []Entry
}

// NewSet builds a Set from entries (injectable for tests; Load reads from disk).
func NewSet(entries []Entry) *Set { return &Set{entries: entries} }

// Listed is one curated entry projected into a listing under a requested dir: its
// path is RELATIVE to that dir, matching ComfyUI's GET /userdata semantics.
type Listed struct {
	RelPath    string
	Size       int
	ModifiedMS int64
}

// ListUnder returns the curated entries visible to id that fall under the logical
// dir, each with RelPath relative to dir. recurse=false keeps only direct
// children (no "/" in the relative path), mirroring ComfyUI's recurse param.
func (s *Set) ListUnder(dir string, recurse bool, id identity.Identity) []Listed {
	dir = strings.Trim(dir, "/")
	var out []Listed
	for _, e := range s.entries {
		if !roleVisible(e.RoleAllowlist, id) {
			continue
		}
		rel, ok := relUnder(e.Path, dir)
		if !ok {
			continue
		}
		if !recurse && strings.Contains(rel, "/") {
			continue
		}
		out = append(out, Listed{RelPath: rel, Size: e.Size, ModifiedMS: e.ModifiedMS})
	}
	return out
}

// Lookup returns the curated entry at logicalPath if it exists AND is visible to
// id. Fail closed: an entry the caller can't see is reported as not found (so a
// curated read/write decision never leaks the existence of an invisible entry).
func (s *Set) Lookup(logicalPath string, id identity.Identity) (Entry, bool) {
	logicalPath = strings.Trim(logicalPath, "/")
	for _, e := range s.entries {
		if e.Path == logicalPath && roleVisible(e.RoleAllowlist, id) {
			return e, true
		}
	}
	return Entry{}, false
}

// Empty reports whether the set has no entries (curation effectively disabled).
func (s *Set) Empty() bool { return s == nil || len(s.entries) == 0 }

// relUnder returns path relative to dir, and whether path is within dir. dir=""
// means the user root (everything is under it).
func relUnder(path, dir string) (string, bool) {
	if dir == "" {
		return path, true
	}
	if path == dir {
		return "", false // dir names a file, not a containing directory
	}
	if rest, ok := strings.CutPrefix(path, dir+"/"); ok {
		return rest, true
	}
	return "", false
}

// roleVisible applies the role_allowlist visibility rule.
func roleVisible(allow []string, id identity.Identity) bool {
	if len(allow) == 0 {
		return true // unrestricted
	}
	for _, a := range allow {
		if rest, ok := strings.CutPrefix(a, "brand:"); ok {
			if id.Class == identity.ClassBrand && id.User == rest {
				return true
			}
			continue
		}
		if id.HasGroup(a) {
			return true
		}
	}
	return false
}
