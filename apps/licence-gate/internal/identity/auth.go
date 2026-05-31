package identity

import (
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

// Identity is who a request is from, resolved from the Envoy-injected auth.
type Identity struct {
	User   string   // Pocket-ID sub (or email fallback); brand → "dopamine-racing"
	Class  Class    // brand / family / service
	Groups []string // Pocket-ID group claims (family-adult, family-minor, mature-content)
}

// HasGroup reports membership in a Pocket-ID group claim. Groups drive curation
// access (e.g. mature-content), NOT the commercial gate (that's Class).
func (i Identity) HasGroup(g string) bool {
	for _, x := range i.Groups {
		if x == g {
			return true
		}
	}
	return false
}

// ParseAuth resolves identity from the Envoy-injected `Authorization: Bearer …`
// header. Two cases:
//   - the bearer equals the configured brand token → brand service-account
//     (Dopamine Racing; always commercial).
//   - otherwise it's a Pocket-ID id_token: Envoy already VERIFIED it upstream,
//     so we only DECODE the JWT payload for claims (sub/email/groups).
func ParseAuth(authHeader, brandToken string) (Identity, error) {
	const prefix = "Bearer "
	if !strings.HasPrefix(authHeader, prefix) {
		return Identity{}, fmt.Errorf("missing or non-Bearer Authorization header")
	}
	tok := strings.TrimSpace(strings.TrimPrefix(authHeader, prefix))
	if tok == "" {
		return Identity{}, fmt.Errorf("empty bearer token")
	}
	// Constant-time compare so a timing side-channel can't reveal the brand
	// service-account token byte-by-byte. The raw token is never logged anywhere
	// (the audit record carries User/Model, never the bearer).
	if brandToken != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(brandToken)) == 1 {
		return Identity{User: "dopamine-racing", Class: ClassBrand}, nil
	}

	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return Identity{}, fmt.Errorf("bearer is not a JWT (want 3 dot-separated segments)")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Identity{}, fmt.Errorf("decode JWT payload: %w", err)
	}
	var claims struct {
		Sub    string   `json:"sub"`
		Email  string   `json:"email"`
		Groups []string `json:"groups"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Identity{}, fmt.Errorf("parse JWT claims: %w", err)
	}
	user := claims.Sub
	if user == "" {
		user = claims.Email
	}
	if user == "" {
		return Identity{}, fmt.Errorf("JWT carries no sub or email claim")
	}
	return Identity{User: user, Class: ClassFamily, Groups: claims.Groups}, nil
}
