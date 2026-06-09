package proxy

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gavinmcfall/containers/apps/licence-gate/internal/identity"
)

func TestIdentify_ForwardedHeadersTrusted_WhenFlagOn(t *testing.T) {
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, nil)
	p.cfg.TrustForwardedHeaders = true

	r := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	r.Header.Set(identity.HeaderForwardedUser, "serina-sub")
	r.Header.Set(identity.HeaderForwardedGroups, "family_adult")
	id, err := p.identify(r)
	if err != nil {
		t.Fatalf("identify: %v", err)
	}
	if id.User != "serina-sub" || id.Class != identity.ClassFamily {
		t.Errorf("got %+v, want family serina-sub", id)
	}
	if !id.HasGroup("family_adult") {
		t.Errorf("groups not carried: %v", id.Groups)
	}
}

func TestIdentify_ForwardedHeadersIgnored_WhenFlagOff(t *testing.T) {
	// Safety: with the flag OFF (Phase-2 default, before oauth2-proxy is in the
	// path), a forged X-Forwarded-User MUST be ignored — identity comes from the
	// verified JWT, not the spoofable header.
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, nil)
	p.cfg.TrustForwardedHeaders = false

	r := httptest.NewRequest(http.MethodGet, "/jobs", nil)
	r.Header.Set(identity.HeaderForwardedUser, "evil-impersonator")
	r.Header.Set("Authorization", "Bearer "+makeJWT("gavin", "family_adult"))
	id, err := p.identify(r)
	if err != nil {
		t.Fatalf("identify: %v", err)
	}
	if id.User != "gavin" {
		t.Errorf("forged X-Forwarded-User honored with flag off: got %q, want gavin", id.User)
	}
}

func TestIdentify_BrandTokenStillWorks_WithFlagOn(t *testing.T) {
	// Worker callbacks hit the gate directly (not via oauth2-proxy) with the brand
	// bearer and NO forwarded headers — must still resolve to the brand identity.
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, nil)
	p.cfg.TrustForwardedHeaders = true

	r := httptest.NewRequest(http.MethodPost, "/distributed/queue", nil)
	r.Header.Set("Authorization", "Bearer brand-secret")
	id, err := p.identify(r)
	if err != nil {
		t.Fatalf("identify: %v", err)
	}
	if id.Class != identity.ClassBrand {
		t.Errorf("brand token not honored: %+v", id)
	}
}
