package isolation

import (
	"net/url"
	"strings"
	"testing"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u
}

func TestRewriteUserdataURL_SingleFile(t *testing.T) {
	in := mustURL(t, "/userdata/test.json")
	got, err := RewriteUserdataURL("alice", in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.EscapedPath() != "/userdata/alice%2Ftest.json" {
		t.Errorf("EscapedPath = %q, want %q", got.EscapedPath(), "/userdata/alice%2Ftest.json")
	}
}

func TestRewriteUserdataURL_PreservesEncodedSlash(t *testing.T) {
	// The frontend encodes "workflows/foo.json" as "workflows%2Ffoo.json" so it
	// fits ComfyUI's single-segment {file} route. Encoding MUST survive forward
	// or master returns 405 (different route shape after decoding).
	in := mustURL(t, "/userdata/workflows%2Ffoo.json")
	got, err := RewriteUserdataURL("alice", in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/userdata/alice%2Fworkflows%2Ffoo.json"
	if got.EscapedPath() != want {
		t.Errorf("EscapedPath = %q, want %q (encoding not preserved)", got.EscapedPath(), want)
	}
}

func TestRewriteUserdataURL_PreservesPercent20InFilename(t *testing.T) {
	// "Test Flow.json" with space encoded — common frontend behavior.
	in := mustURL(t, "/userdata/workflows%2FTest%20Flow.json")
	got, err := RewriteUserdataURL("alice", in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/userdata/alice%2Fworkflows%2FTest%20Flow.json"
	if got.EscapedPath() != want {
		t.Errorf("EscapedPath = %q, want %q", got.EscapedPath(), want)
	}
}

func TestRewriteUserdataURL_PreservesQuery(t *testing.T) {
	in := mustURL(t, "/userdata/x.json?overwrite=true&full_info=false")
	got, err := RewriteUserdataURL("alice", in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Query().Get("overwrite") != "true" || got.Query().Get("full_info") != "false" {
		t.Errorf("query params not preserved: %v", got.RawQuery)
	}
}

func TestRewriteUserdataURL_ListingRoot(t *testing.T) {
	// GET /userdata (no path) — listing endpoint. The user gets a default dir
	// pointing at their bucket root.
	in := mustURL(t, "/userdata")
	got, err := RewriteUserdataURL("alice", in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Query().Get("dir") != "alice" {
		t.Errorf("dir query = %q, want %q", got.Query().Get("dir"), "alice")
	}
}

func TestRewriteUserdataURL_ListingWithDir(t *testing.T) {
	in := mustURL(t, "/userdata?dir=workflows&recurse=true")
	got, err := RewriteUserdataURL("alice", in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Query().Get("dir") != "alice/workflows" {
		t.Errorf("dir query = %q, want %q", got.Query().Get("dir"), "alice/workflows")
	}
	if got.Query().Get("recurse") != "true" {
		t.Errorf("recurse not preserved: %v", got.RawQuery)
	}
}

func TestRewriteUserdataURL_MoveEndpoint(t *testing.T) {
	// POST /userdata/{file}/move/{dest} — both file and dest get scoped so user
	// can't move a file OUT of their bucket via the move endpoint.
	in := mustURL(t, "/userdata/workflows%2Fa.json/move/workflows%2Fb.json")
	got, err := RewriteUserdataURL("alice", in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "/userdata/alice%2Fworkflows%2Fa.json/move/alice%2Fworkflows%2Fb.json"
	if got.EscapedPath() != want {
		t.Errorf("EscapedPath = %q, want %q", got.EscapedPath(), want)
	}
}

func TestRewriteUserdataURL_RejectsTraversal(t *testing.T) {
	for _, in := range []string{
		"/userdata/..%2Fetc%2Fpasswd",
		"/userdata/foo%2F..%2Fbar",
		"/userdata/%2E%2E%2Ffoo", // encoded traversal
	} {
		t.Run(in, func(t *testing.T) {
			_, err := RewriteUserdataURL("alice", mustURL(t, in))
			if err == nil {
				t.Errorf("traversal %q accepted; want rejection", in)
			}
		})
	}
}

func TestRewriteUserdataURL_RejectsAbsolutePath(t *testing.T) {
	_, err := RewriteUserdataURL("alice", mustURL(t, "/userdata/%2Fetc%2Fpasswd"))
	if err == nil {
		t.Error("absolute path accepted; want rejection")
	}
}

func TestRewriteUserdataURL_RejectsBackslash(t *testing.T) {
	in := &url.URL{Path: "/userdata/foo\\bar", RawPath: "/userdata/foo%5Cbar"}
	_, err := RewriteUserdataURL("alice", in)
	if err == nil {
		t.Error("backslash accepted; want rejection")
	}
}

func TestRewriteUserdataURL_RejectsScheme(t *testing.T) {
	in := &url.URL{Path: "/userdata/http://evil/", RawPath: "/userdata/http%3A%2F%2Fevil%2F"}
	_, err := RewriteUserdataURL("alice", in)
	if err == nil {
		t.Error("scheme in segment accepted; want rejection")
	}
}

func TestRewriteUserdataURL_RejectsTraversalInListingDir(t *testing.T) {
	in := mustURL(t, "/userdata?dir=..%2Fetc")
	_, err := RewriteUserdataURL("alice", in)
	if err == nil {
		t.Error("traversal in dir query accepted; want rejection")
	}
}

func TestRewriteUserdataURL_RejectsEmptyUser(t *testing.T) {
	_, err := RewriteUserdataURL("", mustURL(t, "/userdata/foo"))
	if err == nil {
		t.Error("empty user accepted; want rejection")
	}
}

func TestRewriteUserdataURL_RejectsUnsafeUser(t *testing.T) {
	// User identifier ends up in URL paths — reject anything URL-meaningful.
	for _, user := range []string{"alice/bob", "alice..", "alice%20bob", "alice:bob", "alice\\bob", "alice."} {
		t.Run(user, func(t *testing.T) {
			_, err := RewriteUserdataURL(user, mustURL(t, "/userdata/foo"))
			if err == nil {
				t.Errorf("unsafe user %q accepted", user)
			}
		})
	}
}

func TestRewriteUserdataURL_RejectsNonUserdataPath(t *testing.T) {
	_, err := RewriteUserdataURL("alice", mustURL(t, "/prompt"))
	if err == nil || !strings.Contains(err.Error(), "userdata") {
		t.Errorf("non-/userdata path should be rejected, got err=%v", err)
	}
}

func TestRewriteUserdataURL_UUIDUserCommonCase(t *testing.T) {
	// Pocket-ID UUIDs are the common production case.
	uuid := "4f8234ed-8cc7-4d6d-b9f7-f8447a4d5484"
	in := mustURL(t, "/userdata/workflows%2Ffoo.json")
	got, err := RewriteUserdataURL(uuid, in)
	if err != nil {
		t.Fatalf("UUID user rejected: %v", err)
	}
	want := "/userdata/" + uuid + "%2Fworkflows%2Ffoo.json"
	if got.EscapedPath() != want {
		t.Errorf("UUID user case: got %q, want %q", got.EscapedPath(), want)
	}
}
