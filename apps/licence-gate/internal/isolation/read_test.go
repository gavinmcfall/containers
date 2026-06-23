package isolation

import "testing"

// READ-SIDE confidentiality contract (ADR 015, Chat A pre-reviewed gaps 4-5).
// ScopeView confines a ComfyUI /view request (filename + subfolder + type) to
// the caller's own output bucket. Anything else is denied — and the HTTP layer
// returns an IDENTICAL response for denied vs not-found (no existence
// side-channel; enforced in the handler).

func TestScopeViewAllowsOwnFile(t *testing.T) {
	if err := ScopeView("alice", "ComfyUI_00001_.png", "alice", "output"); err != nil {
		t.Fatalf("alice reading her own output should be allowed: %v", err)
	}
}

func TestScopeViewAllowsNestedOwnSubfolder(t *testing.T) {
	if err := ScopeView("alice", "img.png", "alice/batch3", "output"); err != nil {
		t.Fatalf("alice's nested subfolder should be allowed: %v", err)
	}
}

func TestScopeViewDeniesOtherUsersFile(t *testing.T) {
	if err := ScopeView("alice", "secret.png", "bob", "output"); err == nil {
		t.Fatal("alice must NOT read bob's output")
	}
}

func TestScopeViewDeniesOutputRoot(t *testing.T) {
	// Empty subfolder = the shared output root; a user may only see their bucket.
	if err := ScopeView("alice", "anything.png", "", "output"); err == nil {
		t.Fatal("reading the output root (no user bucket) must be denied")
	}
}

func TestScopeViewDeniesPrefixCollision(t *testing.T) {
	// "alice2" must not be readable by "alice" via a prefix trick.
	if err := ScopeView("alice", "x.png", "alice2", "output"); err == nil {
		t.Fatal("subfolder 'alice2' must not satisfy user 'alice'")
	}
}

func TestScopeViewRejectsTraversal(t *testing.T) {
	cases := []struct{ filename, subfolder string }{
		{"x.png", "alice/../bob"},     // traversal in subfolder
		{"../bob/x.png", "alice"},     // traversal in filename
		{"x.png", ".."},               // bare ..
		{"x.png", `alice\..\bob`},     // backslash traversal
		{"x.png", "/etc/passwd"},      // absolute escape
		{"x.png", "alice/sub://evil"}, // scheme
	}
	for _, c := range cases {
		if err := ScopeView("alice", c.filename, c.subfolder, "output"); err == nil {
			t.Errorf("traversal/escape must be rejected: filename=%q subfolder=%q", c.filename, c.subfolder)
		}
	}
}

func TestScopeViewRejectsNonScopableType(t *testing.T) {
	// output + input are user-scopable (2026-06-23); temp and unknown types are
	// not. Type matching is exact/case-sensitive (ComfyUI uses lowercase).
	for _, typ := range []string{"temp", "", "OUTPUT"} {
		if err := ScopeView("alice", "x.png", "alice", typ); err == nil {
			t.Errorf("type %q must be rejected (only 'output'/'input' are read-scopable)", typ)
		}
	}
}

// prompt_id → user ownership, bounded-LRU (Chat A: in-memory OK for Phase 1,
// ~10k cap, evicted → deny, NEVER leak). /history and /queue are keyed by
// prompt_id not user, so the proxy tracks ownership at submit time and filters
// listings by it.

func TestPromptOwnerRoundTrip(t *testing.T) {
	o := NewPromptOwners(10)
	o.Remember("p1", "alice")
	user, known := o.Owner("p1")
	if !known || user != "alice" {
		t.Fatalf("Owner(p1) = (%q, %v), want (alice, true)", user, known)
	}
}

func TestPromptOwnerUnknownDenied(t *testing.T) {
	o := NewPromptOwners(10)
	if _, known := o.Owner("never-seen"); known {
		t.Fatal("unknown prompt_id must report not-known (caller denies)")
	}
}

// The bound trades fetchable history for a memory cap; it NEVER trades
// cross-user leakage — an evicted prompt_id defaults to not-known (deny).
func TestPromptOwnerLRUEvictsOldestToDeny(t *testing.T) {
	o := NewPromptOwners(2)
	o.Remember("p1", "alice")
	o.Remember("p2", "bob")
	o.Remember("p3", "carol") // evicts p1 (oldest)

	if _, known := o.Owner("p1"); known {
		t.Error("evicted p1 must report not-known (deny), never leak")
	}
	if u, known := o.Owner("p2"); !known || u != "bob" {
		t.Errorf("p2 should survive: (%q,%v)", u, known)
	}
	if u, known := o.Owner("p3"); !known || u != "carol" {
		t.Errorf("p3 should be present: (%q,%v)", u, known)
	}
}

// Re-remembering an existing prompt_id refreshes its recency (doesn't double-count).
func TestPromptOwnerReRememberRefreshes(t *testing.T) {
	o := NewPromptOwners(2)
	o.Remember("p1", "alice")
	o.Remember("p2", "bob")
	o.Remember("p1", "alice") // touch p1 → p2 now oldest
	o.Remember("p3", "carol") // evicts p2, not p1

	if _, known := o.Owner("p1"); !known {
		t.Error("recently-touched p1 should survive eviction")
	}
	if _, known := o.Owner("p2"); known {
		t.Error("p2 should have been evicted")
	}
}

// Input uploads are user-scoped the same way outputs are (per-user isolation,
// 2026-06-23): a caller may /view their OWN /input/<user>/ files but not another
// user's, and bare top-level inputs are not user-scoped (denied). temp stays
// unscopable.
func TestScopeViewAllowsOwnInput(t *testing.T) {
	if err := ScopeView("alice", "cat.png", "alice", "input"); err != nil {
		t.Fatalf("own input view should be allowed: %v", err)
	}
}

func TestScopeViewDeniesOtherUsersInput(t *testing.T) {
	if err := ScopeView("alice", "secret.png", "bob", "input"); err == nil {
		t.Fatal("viewing another user's input must be denied")
	}
}

func TestScopeViewDeniesBareTopLevelInput(t *testing.T) {
	if err := ScopeView("alice", "example.png", "", "input"); err == nil {
		t.Fatal("bare top-level input (no user subfolder) must be denied")
	}
}

func TestScopeViewDeniesTempType(t *testing.T) {
	if err := ScopeView("alice", "x.png", "alice", "temp"); err == nil {
		t.Fatal("temp type must remain unscopable (denied)")
	}
}
