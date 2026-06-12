package proxy

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// syntheticJobID mirrors jobsFromDisk's id scheme: base64url("<subfolder>/<name>").
func syntheticJobID(subfolder, name string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(subfolder + "/" + name))
}

// outputTree builds a temp /output with per-user render files and returns its root.
func outputTree(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	for user, files := range map[string][]string{
		"gavin": {"a_00001_.png", "b_00001_.png"},
		"alice": {"secret_00001_.png"},
	} {
		if err := os.MkdirAll(filepath.Join(tmp, user), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if err := os.WriteFile(filepath.Join(tmp, user, f), []byte("PNG"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return tmp
}

func postHistoryDelete(p *Proxy, jwt string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/history", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Content-Type", "application/json")
	return do(p, req)
}

// The assets-gallery delete (frontend 1.43.18 useMediaAssetActions →
// api.deleteItem('history', jobId) → POST /api/history {"delete":[id]}) must
// remove the underlying render file — otherwise the durable disk-scan gallery
// re-lists it on the next refresh and the image "reappears".
func TestHistoryDeleteSyntheticIDRemovesOwnFile(t *testing.T) {
	tmp := outputTree(t)
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, nil)
	p.cfg.OutputDir = tmp

	target := filepath.Join(tmp, "gavin", "a_00001_.png")
	rec := postHistoryDelete(p, makeJWT("gavin"), `{"delete":["`+syntheticJobID("gavin", "a_00001_.png")+`"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("file still on disk after delete: %v", err)
	}
	// Sibling file untouched.
	if _, err := os.Stat(filepath.Join(tmp, "gavin", "b_00001_.png")); err != nil {
		t.Fatalf("unrelated file disturbed: %v", err)
	}
	// Synthetic ids mean nothing to master — must not be forwarded.
	if fc.historyPostCalls != 0 {
		t.Fatalf("synthetic delete leaked upstream: %d calls, body=%s", fc.historyPostCalls, fc.lastHistoryPostBody)
	}
}

// Deleting another user's render must fail closed: 403, file intact, nothing forwarded.
func TestHistoryDeleteOtherUsersFileDenied(t *testing.T) {
	tmp := outputTree(t)
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, nil)
	p.cfg.OutputDir = tmp

	rec := postHistoryDelete(p, makeJWT("gavin"), `{"delete":["`+syntheticJobID("alice", "secret_00001_.png")+`"]}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(tmp, "alice", "secret_00001_.png")); err != nil {
		t.Fatalf("alice's file disturbed: %v", err)
	}
	if fc.historyPostCalls != 0 {
		t.Fatalf("denied delete leaked upstream")
	}
}

// Ids that decode to escaping or non-image paths are rejected before any disk op.
func TestHistoryDeleteTraversalAndNonImageRejected(t *testing.T) {
	tmp := outputTree(t)
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, nil)
	p.cfg.OutputDir = tmp

	for _, raw := range []string{
		"gavin/../alice/secret_00001_.png", // traversal inside the id
		"../gavin/a_00001_.png",            // escapes the output root
		"gavin/notes.txt",                  // not a render image
	} {
		id := base64.RawURLEncoding.EncodeToString([]byte(raw))
		rec := postHistoryDelete(p, makeJWT("gavin"), `{"delete":["`+id+`"]}`)
		if rec.Code != http.StatusForbidden {
			t.Errorf("id %q: status=%d want 403", raw, rec.Code)
		}
	}
	if _, err := os.Stat(filepath.Join(tmp, "alice", "secret_00001_.png")); err != nil {
		t.Fatalf("traversal reached alice's file: %v", err)
	}
	if fc.historyPostCalls != 0 {
		t.Fatalf("rejected ids leaked upstream")
	}
}

// Deleting an already-gone own file is idempotent (the frontend retries optimistically).
func TestHistoryDeleteMissingOwnFileIdempotent(t *testing.T) {
	tmp := outputTree(t)
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, nil)
	p.cfg.OutputDir = tmp

	rec := postHistoryDelete(p, makeJWT("gavin"), `{"delete":["`+syntheticJobID("gavin", "gone_00001_.png")+`"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (idempotent)", rec.Code)
	}
}

// A live job id (real prompt_id, not a synthetic file id) still reaches master —
// the queue panel deletes in-memory history entries this way — but only for the
// owner; someone else's prompt_id is dropped.
func TestHistoryDeleteLivePromptIDForwardedOnlyForOwner(t *testing.T) {
	tmp := outputTree(t)
	fc := &fakeComfy{}
	p, _, _, owners := newTestProxy(t, fc, nil)
	p.cfg.OutputDir = tmp
	owners.Remember("pid-123", "gavin")

	rec := postHistoryDelete(p, makeJWT("gavin"), `{"delete":["pid-123"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner delete status=%d", rec.Code)
	}
	if fc.historyPostCalls != 1 {
		t.Fatalf("owned prompt_id not forwarded: calls=%d", fc.historyPostCalls)
	}
	if !bytes.Contains(fc.lastHistoryPostBody, []byte("pid-123")) {
		t.Fatalf("forwarded body lost the id: %s", fc.lastHistoryPostBody)
	}

	rec = postHistoryDelete(p, makeJWT("mallory"), `{"delete":["pid-123"]}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-owner delete status=%d want 403", rec.Code)
	}
	if fc.historyPostCalls != 1 {
		t.Fatalf("non-owner prompt_id leaked upstream: calls=%d", fc.historyPostCalls)
	}
}

// {"clear":true} wipes master's GLOBAL in-memory history (every user's) — never forward it.
func TestHistoryClearNotForwarded(t *testing.T) {
	tmp := outputTree(t)
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, nil)
	p.cfg.OutputDir = tmp

	rec := postHistoryDelete(p, makeJWT("gavin"), `{"clear":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if fc.historyPostCalls != 0 {
		t.Fatalf("clear leaked upstream (global wipe)")
	}
}

func TestHistoryDeleteMissingAuthRejected(t *testing.T) {
	tmp := outputTree(t)
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, nil)
	p.cfg.OutputDir = tmp

	req := httptest.NewRequest(http.MethodPost, "/api/history", bytes.NewBufferString(`{"delete":["x"]}`))
	rec := do(p, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(tmp, "gavin", "a_00001_.png")); err != nil {
		t.Fatalf("unauthenticated request touched disk: %v", err)
	}
}
