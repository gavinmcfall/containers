package identity

import (
	"net/http"
	"strings"
)

// Forwarded-identity header names, as emitted by a forward-auth proxy
// (oauth2-proxy --pass-user-headers in reverse-proxy mode).
const (
	HeaderForwardedUser   = "X-Forwarded-User"
	HeaderForwardedGroups = "X-Forwarded-Groups"
	HeaderForwardedEmail  = "X-Forwarded-Email"
)

// FromForwardedHeaders resolves a family Identity from the forward-auth proxy's
// injected headers. The proxy has already authenticated the user against the IdP
// and overwrites these headers (a client cannot forge them through it), so — like
// the decode-only JWT path — the gate trusts them. Returns ok=false (fail closed)
// when X-Forwarded-User is absent.
//
// CALLER MUST gate this on TrustForwardedHeaders: it is only safe once the
// forward-auth proxy is in the request path (stripping client-supplied
// X-Forwarded-* and setting its own). Before that, a direct client could forge
// these — see proxy.identify.
func FromForwardedHeaders(h http.Header) (Identity, bool) {
	user := strings.TrimSpace(h.Get(HeaderForwardedUser))
	if user == "" {
		return Identity{}, false
	}
	return Identity{
		User:   user,
		Class:  ClassFamily,
		Groups: splitGroups(h.Get(HeaderForwardedGroups)),
	}, true
}

// splitGroups parses oauth2-proxy's comma-separated X-Forwarded-Groups into a
// trimmed, empty-dropped slice.
func splitGroups(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if g := strings.TrimSpace(p); g != "" {
			out = append(out, g)
		}
	}
	return out
}
