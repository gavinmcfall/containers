package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gavinmcfall/containers/apps/licence-gate/internal/curation"
)

// curatedSet is the test curation: a family-visible portrait + a mature-only
// graph, both under workflows/lighthouse/.
func curatedSet() *curation.Set {
	return curation.NewSet([]curation.Entry{
		{Path: "workflows/lighthouse/portrait.json", RoleAllowlist: []string{"family-adult", "family-minor"}, Content: []byte(`{"curated":"portrait"}`), Size: 22, ModifiedMS: 1000},
		{Path: "workflows/lighthouse/mature.json", RoleAllowlist: []string{"mature-content"}, Content: []byte(`{"curated":"mature"}`), Size: 20, ModifiedMS: 2000},
	})
}

// listingPaths unmarshals a full_info listing body and returns the set of paths.
func listingPaths(t *testing.T, body []byte) map[string]bool {
	t.Helper()
	var entries []struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(body, &entries); err != nil {
		t.Fatalf("listing body not a full_info array: %v (body=%s)", err, body)
	}
	out := make(map[string]bool, len(entries))
	for _, e := range entries {
		out[e.Path] = true
	}
	return out
}

func TestCuratedListingMergedAndRoleFiltered(t *testing.T) {
	fc := &fakeComfy{userdataListJSON: `[{"path":"mine.json","size":5,"modified":100,"created":50}]`}
	p, _, _, _ := newTestProxy(t, fc, nil)
	p.cfg.Curation = curatedSet()

	req := httptest.NewRequest(http.MethodGet, "/api/userdata?dir=workflows&recurse=true&split=false&full_info=true", nil)
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin", "family-adult"))
	rec := do(p, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	paths := listingPaths(t, rec.Body.Bytes())
	if !paths["mine.json"] {
		t.Errorf("user's own workflow missing from merged listing: %v", paths)
	}
	if !paths["lighthouse/portrait.json"] {
		t.Errorf("curated portrait missing (should be relative to dir=workflows): %v", paths)
	}
	if paths["lighthouse/mature.json"] {
		t.Errorf("adult without mature-content must NOT see mature curated workflow: %v", paths)
	}
}

func TestCuratedListingFreshUser404BecomesCuratedOnly(t *testing.T) {
	// Fresh user: their <user>/workflows dir doesn't exist → master 404s the
	// listing. Curation must still surface (200 with just the curated entries).
	fc := &fakeComfy{userdataListStatus: http.StatusNotFound, userdataListJSON: `{"error":"not found"}`}
	p, _, _, _ := newTestProxy(t, fc, nil)
	p.cfg.Curation = curatedSet()

	req := httptest.NewRequest(http.MethodGet, "/api/userdata?dir=workflows&recurse=true&split=false&full_info=true", nil)
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin", "family-adult"))
	rec := do(p, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("fresh-user listing should be 200 with curated entries, got %d body %s", rec.Code, rec.Body.String())
	}
	paths := listingPaths(t, rec.Body.Bytes())
	if !paths["lighthouse/portrait.json"] {
		t.Errorf("curated entry missing for fresh user: %v", paths)
	}
}

func TestCuratedReadServedFromConfigNotMaster(t *testing.T) {
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, nil)
	p.cfg.Curation = curatedSet()

	req := httptest.NewRequest(http.MethodGet, "/api/userdata/workflows%2Flighthouse%2Fportrait.json", nil)
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin", "family-adult"))
	rec := do(p, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("curated read status %d", rec.Code)
	}
	if rec.Body.String() != `{"curated":"portrait"}` {
		t.Errorf("curated read body = %q, want the configMap content", rec.Body.String())
	}
	if fc.userdataCalls != 0 {
		t.Errorf("curated read must NOT hit master, calls=%d", fc.userdataCalls)
	}
}

func TestCuratedWriteRejectedReadOnly(t *testing.T) {
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, nil)
	p.cfg.Curation = curatedSet()

	for _, m := range []string{http.MethodPost, http.MethodDelete} {
		req := httptest.NewRequest(m, "/api/userdata/workflows%2Flighthouse%2Fportrait.json", nil)
		req.Header.Set("Authorization", "Bearer "+makeJWT("gavin", "family-adult"))
		rec := do(p, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s to curated path should be 403, got %d", m, rec.Code)
		}
	}
	if fc.userdataCalls != 0 {
		t.Errorf("rejected curated write must NOT hit master, calls=%d", fc.userdataCalls)
	}
}

func TestNonCuratedUserdataStillForwarded(t *testing.T) {
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, nil)
	p.cfg.Curation = curatedSet()

	req := httptest.NewRequest(http.MethodGet, "/api/userdata/workflows%2Fmine.json", nil)
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin", "family-adult"))
	rec := do(p, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("non-curated read status %d", rec.Code)
	}
	if fc.userdataCalls != 1 {
		t.Errorf("non-curated read should be forwarded to master, calls=%d", fc.userdataCalls)
	}
	// The fake master strips the /api mirror prefix before recording (as the real
	// master serves both spellings); the proxy preserves /api on the wire.
	want := "/userdata/gavin%2Fworkflows%2Fmine.json"
	if fc.lastUserdataRawPath != want {
		t.Errorf("non-curated path scoping: master saw %q, want %q", fc.lastUserdataRawPath, want)
	}
}

func TestCuratedReadInvisibleToRoleFailsClosed(t *testing.T) {
	// A minor requesting the mature curated path: Lookup fails (not visible), so it
	// must NOT be served from config — it falls through to the user's own bucket
	// (forwarded to master), which in reality 404s. Proves no cross-role leak.
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, nil)
	p.cfg.Curation = curatedSet()

	req := httptest.NewRequest(http.MethodGet, "/api/userdata/workflows%2Flighthouse%2Fmature.json", nil)
	req.Header.Set("Authorization", "Bearer "+makeJWT("kid", "family-minor"))
	rec := do(p, req)
	if rec.Body.String() == `{"curated":"mature"}` {
		t.Errorf("mature curated content leaked to a family-minor caller")
	}
	if fc.userdataCalls != 1 {
		t.Errorf("invisible curated path should fall through to master, calls=%d", fc.userdataCalls)
	}
}
