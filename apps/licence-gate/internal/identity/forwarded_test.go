package identity

import (
	"net/http"
	"testing"
)

func TestFromForwardedHeaders_ParsesUserAndGroups(t *testing.T) {
	h := http.Header{}
	h.Set("X-Forwarded-User", "00000000-0000-4000-8000-000000000000")
	h.Set("X-Forwarded-Groups", "family_adult,content-mature")
	h.Set("X-Forwarded-Email", "gavin@example.test")

	id, ok := FromForwardedHeaders(h)
	if !ok {
		t.Fatalf("expected ok with X-Forwarded-User present")
	}
	if id.User != "00000000-0000-4000-8000-000000000000" {
		t.Errorf("user = %q", id.User)
	}
	if id.Class != ClassFamily {
		t.Errorf("class = %q, want family", id.Class)
	}
	if !id.HasGroup("family_adult") || !id.HasGroup("content-mature") {
		t.Errorf("groups not parsed: %v", id.Groups)
	}
}

func TestFromForwardedHeaders_NoUserIsNotOk(t *testing.T) {
	h := http.Header{}
	h.Set("X-Forwarded-Groups", "family_adult")
	if _, ok := FromForwardedHeaders(h); ok {
		t.Errorf("no X-Forwarded-User must yield ok=false (fail closed)")
	}
}

func TestFromForwardedHeaders_GroupsTrimmedAndEmptiesDropped(t *testing.T) {
	h := http.Header{}
	h.Set("X-Forwarded-User", "alice")
	h.Set("X-Forwarded-Groups", " family_adult , , content-mature ")
	id, ok := FromForwardedHeaders(h)
	if !ok {
		t.Fatal("ok")
	}
	if len(id.Groups) != 2 || !id.HasGroup("family_adult") || !id.HasGroup("content-mature") {
		t.Errorf("groups = %v, want [family_adult content-mature] trimmed", id.Groups)
	}
}

func TestFromForwardedHeaders_NoGroupsHeaderIsEmptySlice(t *testing.T) {
	h := http.Header{}
	h.Set("X-Forwarded-User", "alice")
	id, ok := FromForwardedHeaders(h)
	if !ok || len(id.Groups) != 0 {
		t.Errorf("no groups header → empty groups; got ok=%v groups=%v", ok, id.Groups)
	}
}
