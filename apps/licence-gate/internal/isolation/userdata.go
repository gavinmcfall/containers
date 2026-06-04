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
// Preserves URL encoding (specifically %2F-encoded slashes that ComfyUI's frontend
// uses to pack multi-segment paths into the single {file} route parameter).
// Without preservation, Go's httputil reverse proxy decodes %2F→/, master sees the
// path as multi-segment, master returns 405 because the POST route is /userdata/{file}
// (single segment) — Lighthouse Plan-1b 2026-06-03 smoke surfaced this.
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
// Operates on the EscapedPath so %2F-encoded segment separators survive forward.
func rewriteFileEndpoint(user string, rawPath string, out *url.URL) (*url.URL, error) {
	suffix := rawPath[len(userdataPrefix+"/"):] // everything after "/userdata/"
	if suffix == "" {
		return nil, fmt.Errorf("empty file segment after /userdata/")
	}

	// Detect the move sub-route. The /move/ separator uses real slashes because
	// it's a literal in master's route pattern, NOT part of the {file} value.
	if i := strings.Index(suffix, "/move/"); i >= 0 {
		filePart := suffix[:i]
		destPart := suffix[i+len("/move/"):]
		if err := validateEncodedSegment(filePart); err != nil {
			return nil, fmt.Errorf("file segment: %w", err)
		}
		if err := validateEncodedSegment(destPart); err != nil {
			return nil, fmt.Errorf("dest segment: %w", err)
		}
		out.RawPath = userdataPrefix + "/" + url.PathEscape(user) + "%2F" + filePart +
			"/move/" + url.PathEscape(user) + "%2F" + destPart
	} else {
		if err := validateEncodedSegment(suffix); err != nil {
			return nil, fmt.Errorf("file segment: %w", err)
		}
		out.RawPath = userdataPrefix + "/" + url.PathEscape(user) + "%2F" + suffix
	}

	// Set decoded Path too, for consistency. The HTTP client/server pair prefers
	// RawPath when set; Path is the fallback for round-trip safety.
	decoded, err := url.PathUnescape(out.RawPath)
	if err != nil {
		return nil, fmt.Errorf("decoded path malformed: %w", err)
	}
	out.Path = decoded
	return out, nil
}

// validateEncodedSegment rejects unsafe content in a still-URL-encoded path segment.
// Decodes once before checking so encoded traversal (%2E%2E) is caught.
func validateEncodedSegment(s string) error {
	decoded, err := url.PathUnescape(s)
	if err != nil {
		return fmt.Errorf("malformed URL encoding: %w", err)
	}
	return validateDecodedSegment(decoded)
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
