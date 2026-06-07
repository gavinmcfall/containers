package isolation

import "testing"

func TestParseUserdataRequest_Listing(t *testing.T) {
	for _, raw := range []string{"/userdata?dir=workflows&recurse=true", "/api/userdata?dir=workflows&recurse=true"} {
		got, ok := ParseUserdataRequest(mustURL(t, raw))
		if !ok || !got.Listing {
			t.Fatalf("%s: want listing, got %+v ok=%v", raw, got, ok)
		}
		if got.Dir != "workflows" {
			t.Errorf("%s: dir = %q, want workflows", raw, got.Dir)
		}
	}
}

func TestParseUserdataRequest_ListingRootEmptyDir(t *testing.T) {
	got, ok := ParseUserdataRequest(mustURL(t, "/userdata"))
	if !ok || !got.Listing || got.Dir != "" {
		t.Errorf("root listing: got %+v ok=%v", got, ok)
	}
}

func TestParseUserdataRequest_FileEncoded(t *testing.T) {
	got, ok := ParseUserdataRequest(mustURL(t, "/userdata/workflows%2Ffoo.json"))
	if !ok || got.Listing {
		t.Fatalf("want file endpoint, got %+v ok=%v", got, ok)
	}
	if got.File != "workflows/foo.json" {
		t.Errorf("file = %q, want workflows/foo.json", got.File)
	}
}

func TestParseUserdataRequest_FileApiPrefix(t *testing.T) {
	got, _ := ParseUserdataRequest(mustURL(t, "/api/userdata/workflows%2Ffoo.json"))
	if got.File != "workflows/foo.json" {
		t.Errorf("file = %q, want workflows/foo.json", got.File)
	}
}

func TestParseUserdataRequest_FileEnvoyDecoded(t *testing.T) {
	// Envoy already unescaped %2F -> / ; %20 may remain in RawPath.
	in := envoyDecoded(t, "/userdata/workflows/Test Flow.json", "/userdata/workflows/Test%20Flow.json")
	got, ok := ParseUserdataRequest(in)
	if !ok || got.File != "workflows/Test Flow.json" {
		t.Errorf("envoy-decoded file = %q ok=%v, want workflows/Test Flow.json", got.File, ok)
	}
}

func TestParseUserdataRequest_Move(t *testing.T) {
	got, _ := ParseUserdataRequest(mustURL(t, "/userdata/workflows%2Fa.json/move/workflows%2Fb.json"))
	if !got.Move {
		t.Errorf("want move=true, got %+v", got)
	}
	if got.File != "workflows/a.json" {
		t.Errorf("move file = %q, want workflows/a.json", got.File)
	}
}

func TestParseUserdataRequest_NotUserdata(t *testing.T) {
	if _, ok := ParseUserdataRequest(mustURL(t, "/prompt")); ok {
		t.Errorf("/prompt should not parse as userdata")
	}
}
