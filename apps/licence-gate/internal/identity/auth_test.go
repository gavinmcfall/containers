package identity

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

// makeJWT builds a header.payload.sig token with the given claims. The signature
// is bogus on purpose — the proxy DECODES claims (Envoy already verified the
// token upstream), it does not re-verify, so a bad signature must not matter.
func makeJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	b64 := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	return b64(map[string]any{"alg": "RS256", "typ": "JWT"}) + "." + b64(claims) + ".bogus-signature"
}

// The brand (Dopamine Racing) service-account authenticates with a shared bearer
// token, not a Pocket-ID JWT → identity class is brand (always commercial).
func TestParseAuthBrandToken(t *testing.T) {
	const brandToken = "dr-secret-token-xyz"
	id, err := ParseAuth("Bearer "+brandToken, brandToken)
	if err != nil {
		t.Fatalf("ParseAuth(brand): %v", err)
	}
	if id.Class != ClassBrand {
		t.Errorf("class = %q, want brand", id.Class)
	}
}

// A family member authenticates via the Envoy-injected Pocket-ID JWT; class is
// family, user is the sub claim, and group claims (family-adult / mature-content)
// are surfaced for curation access — NOT for the commercial gate.
func TestParseAuthFamilyJWT(t *testing.T) {
	tok := makeJWT(t, map[string]any{
		"sub":    "gavin",
		"email":  "gavin@nerdz.cloud",
		"groups": []string{"family-adult", "mature-content"},
	})
	id, err := ParseAuth("Bearer "+tok, "some-other-brand-token")
	if err != nil {
		t.Fatalf("ParseAuth(family): %v", err)
	}
	if id.Class != ClassFamily {
		t.Errorf("class = %q, want family", id.Class)
	}
	if id.User != "gavin" {
		t.Errorf("user = %q, want gavin", id.User)
	}
	if !id.HasGroup("family-adult") || !id.HasGroup("mature-content") {
		t.Errorf("groups not surfaced: %v", id.Groups)
	}
	if id.HasGroup("family-minor") {
		t.Errorf("should not report a group it doesn't have")
	}
}

// A bad signature must NOT fail parsing — Envoy is the verifier; we only decode.
func TestParseAuthIgnoresSignature(t *testing.T) {
	tok := makeJWT(t, map[string]any{"sub": "kid", "groups": []string{"family-minor"}})
	id, err := ParseAuth("Bearer "+tok, "")
	if err != nil {
		t.Fatalf("decode-only parse should not fail on signature: %v", err)
	}
	if id.User != "kid" || !id.HasGroup("family-minor") {
		t.Errorf("unexpected identity: %+v", id)
	}
}

func TestParseAuthRejectsMissingOrMalformed(t *testing.T) {
	for _, h := range []string{"", "Bearer ", "Bearer not-a-jwt", "Basic abc", "Bearer a.b"} {
		if _, err := ParseAuth(h, "brand-token"); err == nil {
			t.Errorf("malformed/missing auth %q must error", h)
		}
	}
}
