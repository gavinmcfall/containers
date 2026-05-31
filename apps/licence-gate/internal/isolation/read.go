package isolation

import (
	"container/list"
	"fmt"
	"path"
	"strings"
	"sync"
)

// ScopeView confines a ComfyUI /view request to the caller's own output bucket
// (ADR 015 read-side). The file served is type-dir/<subfolder>/<filename>; a
// user may read only files whose first path segment is their own user id, and
// only from the `output` type (input/temp are not user-scoped reads in Phase 1).
// Any traversal/escape (`..`, backslash, scheme, absolute) is rejected. The HTTP
// layer must return an identical response for an error here vs a genuine 404, so
// existence of another user's file cannot be probed.
func ScopeView(user, filename, subfolder, typ string) error {
	if typ != "output" {
		return fmt.Errorf("view type %q is not user-scopable (only output)", typ)
	}
	for _, seg := range []string{filename, subfolder} {
		if strings.ContainsAny(seg, `\`) || strings.Contains(seg, "..") ||
			strings.Contains(seg, "://") || strings.HasPrefix(seg, "/") {
			return fmt.Errorf("unsafe view path segment %q", seg)
		}
	}
	rel := path.Clean(path.Join(subfolder, filename))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return fmt.Errorf("view path escapes the output store: %q", rel)
	}
	first := rel
	if i := strings.IndexByte(rel, '/'); i >= 0 {
		first = rel[:i]
	}
	if first != user {
		return fmt.Errorf("view path %q is not in %q's bucket", rel, user)
	}
	return nil
}

// PromptOwners tracks prompt_id → user ownership so /history and /queue (keyed
// by prompt_id, not user) can be filtered to the caller. In-memory, bounded LRU
// (ADR 015 / Chat A: no persistence in Phase 1). The bound trades fetchable
// history for a memory cap; an evicted (or never-seen) prompt_id reports
// not-known so the caller DENIES — it never trades cross-user leakage.
type PromptOwners struct {
	mu  sync.Mutex
	cap int
	ll  *list.List // front = most-recently remembered
	m   map[string]*list.Element
}

type ownerEntry struct {
	promptID string
	user     string
}

// NewPromptOwners returns an owner map bounded to capacity entries.
func NewPromptOwners(capacity int) *PromptOwners {
	if capacity < 1 {
		capacity = 1
	}
	return &PromptOwners{cap: capacity, ll: list.New(), m: make(map[string]*list.Element)}
}

// Remember records (or refreshes) the owner of a prompt_id, evicting the oldest
// entry if at capacity.
func (p *PromptOwners) Remember(promptID, user string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if el, ok := p.m[promptID]; ok {
		el.Value.(*ownerEntry).user = user
		p.ll.MoveToFront(el)
		return
	}
	p.m[promptID] = p.ll.PushFront(&ownerEntry{promptID: promptID, user: user})
	if p.ll.Len() > p.cap {
		if oldest := p.ll.Back(); oldest != nil {
			p.ll.Remove(oldest)
			delete(p.m, oldest.Value.(*ownerEntry).promptID)
		}
	}
}

// Owner returns the recorded owner of a prompt_id. known=false (evicted or never
// seen) means the caller must deny — never assume ownership.
func (p *PromptOwners) Owner(promptID string) (user string, known bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	el, ok := p.m[promptID]
	if !ok {
		return "", false
	}
	return el.Value.(*ownerEntry).user, true
}
