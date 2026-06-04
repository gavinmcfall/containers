package isolation

import (
	"fmt"
	"net/url"
	"strings"
)

// userdataPrefix is the URL prefix for ComfyUI's userdata routes.
const userdataPrefix = "/userdata"

// RewriteUserdataURL scopes a /userdata/... URL under the caller's bucket. Mirrors
// the write-side of RewriteOutputs but for ComfyUI's userdata storage (workflow
// persistence, etc.). The user identifier is injected as the leading directory in
// the userdata path, so caller A's "workflows/foo.json" becomes "A/workflows/foo.json"
// from master's perspective; caller B never sees A's files.
//
// ComfyUI's frontend packs multi-segment paths into the single {file} route
// parameter using %2F-encoded slashes (e.g. "workflows%2Ffoo.json"). master's
// route is /userdata/{file} — a SINGLE segment — so the whole file path must
// reach master as one %2F-encoded segment or it returns 405/404 (route mismatch).
//
// CRITICAL: this rewrite must NOT assume the inbound %2F survived. HTTP
// intermediaries are allowed to normalize %2F→/ (RFC 3986 §2.2 treats them as
// equivalent in some contexts). Envoy Gateway in particular runs
// path_with_escaped_slashes_action=UNESCAPE_AND_REDIRECT by default, so by the
// time a browser request reaches this proxy the %2F is already a literal slash.
// We therefore work on the FULLY DECODED logical path and re-encode the entire
// file path as one segment (url.PathEscape maps /→%2F). This produces the
// identical, correct single-segment result whether the inbound path arrived
// encoded (curl/internal) or decoded (browser via Envoy). Lighthouse Plan-1b
// 2026-06-04 smoke surfaced the decoded-slash case as a 405-on-save.
//
// Handles the four ComfyUI userdata route shapes:
//   - GET /userdata                          (listing; rewrite ?dir= query param)
//   - GET /userdata/{file}                   (read; rewrite {file})
//   - POST /userdata/{file}                  (write; rewrite {file})
//   - DELETE /userdata/{file}                (delete; rewrite {file})
//   - POST /userdata/{file}/move/{dest}      (move; rewrite both {file} and {dest})
//
// Validation rejects unsafe path segments (.. / traversal / scheme / backslash)
// AFTER URL-decoding (so encoded traversal %2E%2E is caught).
func RewriteUserdataURL(user string, in *url.URL) (*url.URL, error) {
	if user == "" {
		return nil, fmt.Errorf("empty user (proxy bug — identify() should have rejected)")
	}
	if strings.ContainsAny(user, "/\\.:%") {
		// The user identifier ends up in URL paths; reject anything that could
		// confuse the path-segment shape we're constructing.
		return nil, fmt.Errorf("unsafe user identifier %q (contains URL-meaningful chars)", user)
	}

	out := *in // copy; caller's URL not mutated
	rawPath := in.EscapedPath()

	switch {
	case rawPath == userdataPrefix, rawPath == userdataPrefix+"/":
		// Listing endpoint — the `dir` query param is what carries the user path.
		return rewriteListingQuery(user, &out)

	case strings.HasPrefix(rawPath, userdataPrefix+"/"):
		// File-level endpoint (read/write/delete/move).
		return rewriteFileEndpoint(user, rawPath, &out)

	default:
		return nil, fmt.Errorf("not a /userdata path: %q", rawPath)
	}
}

// rewriteListingQuery handles GET /userdata?dir=foo by prepending <user>/ to dir.
// An empty dir lists the user's bucket root.
func rewriteListingQuery(user string, out *url.URL) (*url.URL, error) {
	q := out.Query()
	dir := q.Get("dir")
	if err := validateDecodedSegment(dir); err != nil {
		return nil, fmt.Errorf("unsafe dir param: %w", err)
	}
	if dir == "" {
		q.Set("dir", user)
	} else {
		q.Set("dir", user+"/"+strings.TrimPrefix(dir, "/"))
	}
	out.RawQuery = q.Encode()
	return out, nil
}

// rewriteFileEndpoint handles /userdata/{file} and /userdata/{file}/move/{dest}.
// Works on EscapedPath to find the structural /move/ literal, then scopeSegment
// decodes each part fully and re-encodes it as one segment — so the result is
// correct whether the inbound file slashes arrived as %2F or as literal /.
func rewriteFileEndpoint(user string, rawPath string, out *url.URL) (*url.URL, error) {
	suffix := rawPath[len(userdataPrefix+"/"):] // everything after "/userdata/"
	if suffix == "" {
		return nil, fmt.Errorf("empty file segment after /userdata/")
	}

	// Detect the move sub-route. The /move/ separator is a LITERAL in master's
	// route pattern, not part of {file}. In the encoded case the file's own
	// slashes are %2F, so the only literal "/move/" is structural — unambiguous.
	// In the decoded case (Envoy already unescaped) we split on the first
	// "/move/"; a userdata filename literally containing "/move/" is pathological
	// and outside ComfyUI's behaviour.
	if i := strings.Index(suffix, "/move/"); i >= 0 {
		fileSeg, err := scopeSegment(user, suffix[:i])
		if err != nil {
			return nil, fmt.Errorf("file segment: %w", err)
		}
		destSeg, err := scopeSegment(user, suffix[i+len("/move/"):])
		if err != nil {
			return nil, fmt.Errorf("dest segment: %w", err)
		}
		out.RawPath = userdataPrefix + "/" + fileSeg + "/move/" + destSeg
	} else {
		fileSeg, err := scopeSegment(user, suffix)
		if err != nil {
			return nil, fmt.Errorf("file segment: %w", err)
		}
		out.RawPath = userdataPrefix + "/" + fileSeg
	}

	// Set decoded Path too. The HTTP client prefers RawPath when set; Path is the
	// round-trip fallback.
	decoded, err := url.PathUnescape(out.RawPath)
	if err != nil {
		return nil, fmt.Errorf("decoded path malformed: %w", err)
	}
	out.Path = decoded
	return out, nil
}

// scopeSegment turns a single userdata file path (which may carry its internal
// directory separators as %2F OR as already-decoded literal /) into one
// master-ready segment: <user>/<path> percent-encoded so every / becomes %2F.
// It fully decodes first (so validation sees the real path and both encodings
// converge), rejects unsafe content, then re-encodes the whole thing as one
// segment via url.PathEscape (which escapes / to %2F).
func scopeSegment(user, part string) (string, error) {
	decoded, err := url.PathUnescape(part)
	if err != nil {
		return "", fmt.Errorf("malformed URL encoding: %w", err)
	}
	if err := validateDecodedSegment(decoded); err != nil {
		return "", err
	}
	return url.PathEscape(user + "/" + decoded), nil
}

// validateDecodedSegment is the common safety check after decoding (or for
// already-decoded inputs like query-string values).
func validateDecodedSegment(s string) error {
	if s == "" {
		return nil // empty is fine (listing root, etc.)
	}
	if strings.Contains(s, "..") {
		return fmt.Errorf("path traversal: %q", s)
	}
	if strings.ContainsAny(s, `\`) {
		return fmt.Errorf("backslash: %q", s)
	}
	if strings.Contains(s, "://") {
		return fmt.Errorf("scheme: %q", s)
	}
	if strings.HasPrefix(s, "/") {
		return fmt.Errorf("absolute path: %q", s)
	}
	if strings.HasPrefix(s, "~") {
		return fmt.Errorf("home expansion: %q", s)
	}
	return nil
}
