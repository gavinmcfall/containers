package curation

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// manifest is the on-disk curation.json shape (the GitOps-managed registry the
// app-mode-curation design §1 describes, in the minimal form the proxy needs to
// surface + role-scope workflows).
type manifest struct {
	Entries []manifestEntry `json:"entries"`
}

type manifestEntry struct {
	Path          string   `json:"path"`           // logical userdata path (workflows/...)
	File          string   `json:"file"`           // file in the curation dir holding the JSON
	RoleAllowlist []string `json:"role_allowlist"` // claims that may see it; empty = all
}

// Load reads a curation directory: a curation.json manifest plus the referenced
// workflow JSON files. An empty dir, or a dir with no curation.json, is inert
// (Empty set, no error) so the proxy boots even before the configMap is
// populated. A manifest that references a missing file is a startup error —
// curation is GitOps-reviewed, so a dangling reference is a config bug to surface
// loudly, not silently drop.
func Load(dir string) (*Set, error) {
	if dir == "" {
		return NewSet(nil), nil
	}
	manifestPath := filepath.Join(dir, "curation.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return NewSet(nil), nil // dir present but no manifest yet — inert
		}
		return nil, fmt.Errorf("read %s: %w", manifestPath, err)
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", manifestPath, err)
	}

	entries := make([]Entry, 0, len(m.Entries))
	for _, me := range m.Entries {
		if me.Path == "" || me.File == "" {
			return nil, fmt.Errorf("curation entry missing path or file: %+v", me)
		}
		fp := filepath.Join(dir, me.File)
		content, err := os.ReadFile(fp)
		if err != nil {
			return nil, fmt.Errorf("curated workflow %q: %w", me.File, err)
		}
		var modMS int64
		if info, err := os.Stat(fp); err == nil {
			modMS = info.ModTime().UnixMilli()
		}
		entries = append(entries, Entry{
			Path:          me.Path,
			RoleAllowlist: me.RoleAllowlist,
			Content:       content,
			Size:          len(content),
			ModifiedMS:    modMS,
		})
	}
	return NewSet(entries), nil
}
