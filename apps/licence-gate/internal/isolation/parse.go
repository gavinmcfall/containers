package isolation

import (
	"net/url"
	"strings"
)

// UserdataRequest is the decoded LOGICAL view of an inbound /userdata request,
// independent of the /api prefix and of the %2F-encoded vs Envoy-decoded slash
// difference. The proxy uses it to decide whether a request targets the curated
// namespace (served from the curation Set) or the caller's own bucket (scoped +
// forwarded). It does NOT scope to a user — that stays in RewriteUserdataURL.
type UserdataRequest struct {
	Listing bool   // GET /userdata — the directory listing endpoint
	Dir     string // listing: the decoded `dir` query param (e.g. "workflows"); "" = root
	File    string // file endpoints: decoded logical file path (e.g. "workflows/foo.json")
	Move    bool   // file endpoint is a /move/{dest} sub-route
}

// ParseUserdataRequest returns the logical view of in and whether in is a
// /userdata path at all. Mirrors RewriteUserdataURL's prefix/encoding handling so
// the two agree on what a request targets.
func ParseUserdataRequest(in *url.URL) (UserdataRequest, bool) {
	rawPath := in.EscapedPath()
	prefix, ok := matchUserdataPrefix(rawPath)
	if !ok {
		return UserdataRequest{}, false
	}
	if rawPath == prefix || rawPath == prefix+"/" {
		return UserdataRequest{Listing: true, Dir: in.Query().Get("dir")}, true
	}
	suffix := rawPath[len(prefix+"/"):]
	if i := strings.Index(suffix, "/move/"); i >= 0 {
		return UserdataRequest{File: decodeSegment(suffix[:i]), Move: true}, true
	}
	return UserdataRequest{File: decodeSegment(suffix)}, true
}

// decodeSegment fully decodes a userdata file segment to its logical path. %2F
// becomes "/" (so an encoded multi-segment path collapses to its logical form);
// an already-decoded path is returned essentially unchanged. On malformed
// encoding it falls back to the raw input (the caller's safety checks still run
// downstream in RewriteUserdataURL).
func decodeSegment(seg string) string {
	if dec, err := url.PathUnescape(seg); err == nil {
		return dec
	}
	return seg
}
