// Package proxy is the licence-gate reverse proxy: the single HTTP entrypoint in
// front of the ComfyUI master. It wires the policy/isolation/audit packages onto
// the ComfyUI wire protocol (ADR 015). Every render request is authenticated
// (decode-only — Envoy verifies the JWT upstream), licence-gated
// (commercial-by-default), output-scoped to the caller's bucket, and audited;
// reads (/view, /history, /queue) are confined to the caller's own outputs.
//
// Routes not listed below pass through unchanged. Per-user scoping of /ws
// progress and /upload/image is a documented Phase-1 gap (the heavy-tier
// milestone runs with a trusted admin + brand identity); they pass through and
// are tightened in a later plan. /userdata is scoped here (v0.1.3+, Plan 1b).
package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/gavinmcfall/containers/apps/licence-gate/internal/audit"
	"github.com/gavinmcfall/containers/apps/licence-gate/internal/gate"
	"github.com/gavinmcfall/containers/apps/licence-gate/internal/identity"
	"github.com/gavinmcfall/containers/apps/licence-gate/internal/isolation"
	"github.com/gavinmcfall/containers/apps/licence-gate/internal/registry"
	"github.com/gavinmcfall/containers/apps/licence-gate/internal/tier"
	"github.com/gavinmcfall/containers/apps/licence-gate/internal/workflow"
)

// HeaderPersonal is the request header a family caller sets to flag a render
// personal (non-commercial). Absent or non-truthy ⇒ commercial (fail-safe).
const HeaderPersonal = "X-Lighthouse-Personal"

// maxPromptBody caps the POST /prompt body we buffer for inspection.
const maxPromptBody = 8 << 20 // 8 MiB

// Config is the proxy's startup configuration.
type Config struct {
	Upstream   *url.URL           // ComfyUI master base URL (e.g. http://127.0.0.1:8188)
	Allowlist  workflow.Allowlist // CI-generated node allowlist (default-deny)
	Registry   *registry.Registry // licence-tagged model registry
	BrandToken string             // brand service-account bearer (constant-time compared)
	Audit      *audit.Writer      // §6 audit sink (stdout JSON in prod)
	Owners     *isolation.PromptOwners
	Now        func() string // RFC3339 timestamp source (injectable for tests)
	// WorkerTiers maps worker_id -> VRAM tier; nil disables tier filtering (the
	// /distributed/queue enabled_worker_ids pass through untouched).
	WorkerTiers tier.Map
}

// Proxy is the licence-gate HTTP handler.
type Proxy struct {
	cfg         Config
	passthrough *httputil.ReverseProxy
	client      *http.Client
}

// New builds a Proxy from cfg.
func New(cfg Config) *Proxy {
	return &Proxy{
		cfg:         cfg,
		passthrough: httputil.NewSingleHostReverseProxy(cfg.Upstream),
		client:      &http.Client{},
	}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// ComfyUI's web frontend prepends /api to EVERY native API call
	// (frontend api.apiURL(): `path.startsWith("/api") ? base+path : base+"/api"+path`),
	// and master mirrors every route under both "/" and "/api" (server.py registers
	// `"/api"+route.path` for all routes). So a browser hits /api/prompt, /api/view,
	// /api/userdata, … while the ComfyUI-Distributed plugin (its own apiClient) hits
	// /distributed/queue with no prefix. We MUST gate/scope both spellings — matching
	// only the bare path let every browser request fall through to passthrough,
	// bypassing the licence gate AND per-user output isolation (Lighthouse Plan-1b
	// HAR 2026-06-04: /api/userdata save 405 was the visible tip; /api/view, /api/history,
	// /api/queue were silently un-scoped too). Normalise a leading /api segment for
	// MATCHING only; forward() preserves the original r.URL.Path, and master serves
	// both spellings, so no prefix rewrite is needed on the wire.
	routePath := r.URL.Path
	if routePath == "/api" {
		routePath = "/"
	} else if strings.HasPrefix(routePath, "/api/") {
		routePath = strings.TrimPrefix(routePath, "/api")
	}

	switch {
	// Both the plain (/prompt) and the ComfyUI-Distributed GPU render path
	// (/distributed/queue) carry the workflow graph under "prompt" and return a
	// prompt_id — so both go through the same gate→rewrite→remember→audit
	// pipeline. Gating only /prompt would let a render bypass the licence gate by
	// using the distributed endpoint.
	case r.Method == http.MethodPost && (routePath == "/prompt" || routePath == "/distributed/queue"):
		p.handlePrompt(w, r)
	case r.Method == http.MethodGet && routePath == "/view":
		p.handleView(w, r)
	case r.Method == http.MethodGet && routePath == "/history":
		p.handleHistory(w, r)
	case r.Method == http.MethodGet && routePath == "/queue":
		p.handleQueue(w, r)
	// /userdata is ComfyUI's per-file storage (workflow persistence etc). Master
	// has no per-user concept; we inject the caller's bucket into the URL so each
	// user gets an isolated namespace. Covers GET (list+read), POST (write),
	// DELETE (delete), POST .../move/... (rename/move).
	case routePath == "/userdata" || strings.HasPrefix(routePath, "/userdata/"):
		p.handleUserdata(w, r)
	default:
		p.passthrough.ServeHTTP(w, r)
	}
}

// identify resolves the caller from the Envoy-injected Authorization header.
func (p *Proxy) identify(r *http.Request) (identity.Identity, error) {
	return identity.ParseAuth(r.Header.Get("Authorization"), p.cfg.BrandToken)
}

// personalRequested reports whether the caller flagged this render personal.
func personalRequested(r *http.Request) bool {
	v := strings.TrimSpace(r.Header.Get(HeaderPersonal))
	return strings.EqualFold(v, "true") || v == "1"
}

func (p *Proxy) handlePrompt(w http.ResponseWriter, r *http.Request) {
	id, err := p.identify(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	personal := personalRequested(r)
	commercial, rationale := identity.ResolveCommercial(id.Class, personal)

	body, err := io.ReadAll(io.LimitReader(r.Body, maxPromptBody))
	if err != nil {
		http.Error(w, "read body", http.StatusBadRequest)
		return
	}
	graph, err := workflow.Parse(body)
	if err != nil {
		http.Error(w, "invalid prompt: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := graph.Validate(p.cfg.Allowlist); err != nil {
		http.Error(w, "workflow rejected: "+err.Error(), http.StatusBadRequest)
		return
	}

	refs := graph.ModelReferences(p.cfg.Allowlist)
	decision := gate.Evaluate(refs, p.cfg.Registry, gate.Job{Commercial: commercial, Groups: id.Groups})

	if !decision.Allowed {
		p.audit(id, personal, commercial, rationale, refs, decision, "")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"error":      "licence policy rejected this render",
			"action":     decision.Action,
			"violations": decision.Violations,
		})
		return
	}

	// Allowed: scope every saver to the caller's bucket before forwarding.
	if err := isolation.RewriteOutputs(graph, id.User, p.cfg.Allowlist); err != nil {
		http.Error(w, "unsafe output target: "+err.Error(), http.StatusBadRequest)
		return
	}
	rewritten, err := reencodePrompt(body, graph)
	if err != nil {
		http.Error(w, "re-encode prompt", http.StatusInternalServerError)
		return
	}

	// On the distributed render path, drop workers that can't run this job's
	// models from enabled_worker_ids (e.g. a 3050 handed SDXL). Reject if none
	// qualify — never silently fan out to a worker that would OOM.
	if p.cfg.WorkerTiers != nil && strings.HasSuffix(r.URL.Path, "/distributed/queue") {
		rewritten, err = p.applyTierFilter(rewritten, refs)
		if err != nil {
			http.Error(w, "tier: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	resp, err := p.forward(r, rewritten)
	if err != nil {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	// Capture prompt_id so /history and /queue can be owner-filtered.
	if pid := extractPromptID(respBody); pid != "" {
		p.cfg.Owners.Remember(pid, id.User)
	}
	p.audit(id, personal, commercial, rationale, refs, decision, extractPromptID(respBody))

	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)
}

func (p *Proxy) handleView(w http.ResponseWriter, r *http.Request) {
	id, err := p.identify(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	q := r.URL.Query()
	if err := isolation.ScopeView(id.User, q.Get("filename"), q.Get("subfolder"), q.Get("type")); err != nil {
		// Denied — return the canonical 404 (identical to a genuine miss) so the
		// existence of another user's file cannot be probed.
		writeCanonical404(w)
		return
	}
	resp, err := p.forward(r, nil)
	if err != nil {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// Normalise upstream's own 404 body to the canonical one — same bytes a
		// denied request gets, closing the side-channel.
		writeCanonical404(w)
		return
	}
	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (p *Proxy) handleHistory(w http.ResponseWriter, r *http.Request) {
	id, err := p.identify(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	resp, err := p.forward(r, nil)
	if err != nil {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		// Not the object shape we expected — fail closed with an empty object.
		writeJSON(w, http.StatusOK, map[string]json.RawMessage{})
		return
	}
	filtered := make(map[string]json.RawMessage, len(all))
	for pid, entry := range all {
		if owner, known := p.cfg.Owners.Owner(pid); known && owner == id.User {
			filtered[pid] = entry
		}
	}
	writeJSON(w, http.StatusOK, filtered)
}

func (p *Proxy) handleQueue(w http.ResponseWriter, r *http.Request) {
	id, err := p.identify(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	resp, err := p.forward(r, nil)
	if err != nil {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)

	var q struct {
		Running [][]json.RawMessage `json:"queue_running"`
		Pending [][]json.RawMessage `json:"queue_pending"`
	}
	if err := json.Unmarshal(raw, &q); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"queue_running": []any{}, "queue_pending": []any{}})
		return
	}
	out := struct {
		Running [][]json.RawMessage `json:"queue_running"`
		Pending [][]json.RawMessage `json:"queue_pending"`
	}{
		Running: p.filterQueueEntries(q.Running, id.User),
		Pending: p.filterQueueEntries(q.Pending, id.User),
	}
	writeJSON(w, http.StatusOK, out)
}

// filterQueueEntries keeps only queue tuples whose prompt_id (element [1]) the
// caller owns. A malformed tuple (unknown owner included) is dropped.
func (p *Proxy) filterQueueEntries(entries [][]json.RawMessage, user string) [][]json.RawMessage {
	kept := make([][]json.RawMessage, 0, len(entries))
	for _, e := range entries {
		if len(e) < 2 {
			continue
		}
		var pid string
		if err := json.Unmarshal(e[1], &pid); err != nil {
			continue
		}
		if owner, known := p.cfg.Owners.Owner(pid); known && owner == user {
			kept = append(kept, e)
		}
	}
	return kept
}

// handleUserdata scopes /userdata/* to the caller's bucket. The user identifier
// is injected as a leading directory in the userdata path, so caller A's
// "workflows/foo.json" becomes "A/workflows/foo.json" from master's perspective;
// caller B never sees A's files.
//
// Forward uses forwardEncoded — not forward — because ComfyUI's frontend packs
// multi-segment paths into the single {file} route param via %2F encoding, and
// the standard forward path decodes %2F→/ which makes master return 405 (route
// mismatch).
func (p *Proxy) handleUserdata(w http.ResponseWriter, r *http.Request) {
	id, err := p.identify(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	rewritten, err := isolation.RewriteUserdataURL(id.User, r.URL)
	if err != nil {
		// Unsafe path or bad encoding — fail closed.
		http.Error(w, "userdata: "+err.Error(), http.StatusBadRequest)
		return
	}
	resp, err := p.forwardEncoded(r, rewritten)
	if err != nil {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// forwardEncoded sends r to the upstream using a pre-rewritten URL whose
// EscapedPath has been deliberately set (e.g. with %2F-encoded segment
// separators preserved). Unlike forward, this path does NOT decode and rebuild
// the URL via target.Path — that loses the original encoding. Mirrors forward's
// other behavior (header copy, Origin strip).
func (p *Proxy) forwardEncoded(r *http.Request, in *url.URL) (*http.Response, error) {
	target := *p.cfg.Upstream
	// Splice upstream scheme/host/base onto the rewritten path. Construct the
	// target string manually so RawPath survives — http.NewRequest would parse
	// our URL.String() back, which is correct, but we want to be explicit.
	target.RawPath = in.RawPath
	target.Path = in.Path
	target.RawQuery = in.RawQuery

	req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
	if err != nil {
		return nil, err
	}
	copyHeader(req.Header, r.Header)
	req.Header.Del("Origin") // see forward() — same DNS-rebinding 403 concern.
	if r.ContentLength > 0 {
		req.ContentLength = r.ContentLength
	}
	return p.client.Do(req)
}

// forward sends r (with an optional replacement body) to the upstream master and
// returns the raw response for the caller to process.
func (p *Proxy) forward(r *http.Request, body []byte) (*http.Response, error) {
	target := *p.cfg.Upstream
	target.Path = singleJoin(p.cfg.Upstream.Path, r.URL.Path)
	target.RawQuery = r.URL.RawQuery

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	} else if r.Body != nil {
		reader = r.Body
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), reader)
	if err != nil {
		return nil, err
	}
	copyHeader(req.Header, r.Header)
	// ComfyUI has built-in DNS-rebinding protection that returns 403 when the
	// browser-set Origin doesn't match the upstream Host. Behind a reverse proxy,
	// the proxy rewrites Host (to upstream loopback) but Origin would otherwise
	// pass through unchanged — causing every browser-originated request to be
	// rejected. Strip Origin on forward so the upstream sees a same-origin
	// request (its own Host). Discovered Lighthouse smoke 2026-06-03 — curl tests
	// pass (Origin empty), browser-via-OIDC fails (Origin=public-URL) with master
	// log line "request with non matching host and origin ..., returning 403".
	req.Header.Del("Origin")
	if body != nil {
		req.ContentLength = int64(len(body))
		req.Header.Set("Content-Type", "application/json")
	}
	return p.client.Do(req)
}

// audit composes and writes one §6 render record. Model/Licence reflect the
// render-level decision: the first violating model on reject, else the first
// model reference (commercial traceability; richer per-model rows land with the
// CNPG table in Plan 4).
func (p *Proxy) audit(id identity.Identity, personal, commercial bool, rationale string, refs []workflow.ModelRef, d gate.Decision, promptID string) {
	model, licence := "", ""
	if len(d.Violations) > 0 {
		model = d.Violations[0].Filename
	} else if len(refs) > 0 {
		model = refs[0].Filename
	}
	if model != "" {
		if e, ok := p.cfg.Registry.Resolve(model); ok {
			licence = e.Licence
		}
	}
	_ = p.cfg.Audit.Write(audit.Record{
		Timestamp:             p.cfg.Now(),
		User:                  id.User,
		IdentityClass:         string(id.Class),
		PersonalFlagRequested: personal,
		Commercial:            commercial,
		CommercialRationale:   rationale,
		Model:                 model,
		Licence:               licence,
		Decision:              d.Action,
		PromptID:              promptID,
	})
}

// applyTierFilter intersects the distributed envelope's enabled_worker_ids with
// tier-capable workers and writes the filtered list back. Returns an error (→400)
// if the job has a tier constraint no enabled worker satisfies. Envelopes with no
// enabled_worker_ids (master-local) pass through unchanged.
func (p *Proxy) applyTierFilter(body []byte, refs []workflow.ModelRef) ([]byte, error) {
	var env map[string]json.RawMessage
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("envelope parse: %w", err)
	}
	raw, ok := env["enabled_worker_ids"]
	if !ok {
		return body, nil
	}
	var requested []string
	if err := json.Unmarshal(raw, &requested); err != nil {
		return nil, fmt.Errorf("enabled_worker_ids parse: %w", err)
	}
	kept, err := tier.Filter(refs, p.cfg.Registry, p.cfg.WorkerTiers, requested)
	if err != nil {
		return nil, err
	}
	keptRaw, err := json.Marshal(kept)
	if err != nil {
		return nil, fmt.Errorf("re-encode worker ids: %w", err)
	}
	env["enabled_worker_ids"] = keptRaw
	return json.Marshal(env)
}

// reencodePrompt writes the rewritten graph back into the original ComfyUI
// envelope, preserving client_id/extra_data and any other top-level fields.
func reencodePrompt(original []byte, g *workflow.Graph) ([]byte, error) {
	var env map[string]json.RawMessage
	if err := json.Unmarshal(original, &env); err != nil {
		return nil, fmt.Errorf("decode envelope: %w", err)
	}
	nodes, err := json.Marshal(g.Nodes)
	if err != nil {
		return nil, fmt.Errorf("encode graph: %w", err)
	}
	env["prompt"] = nodes
	return json.Marshal(env)
}

// extractPromptID reads the prompt_id from a ComfyUI /prompt response.
func extractPromptID(body []byte) string {
	var r struct {
		PromptID string `json:"prompt_id"`
	}
	_ = json.Unmarshal(body, &r)
	return r.PromptID
}

// writeCanonical404 emits the exact bytes of Go's default 404 (matching what
// denied and genuinely-missing /view requests both return).
func writeCanonical404(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(w, "404 page not found\n")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// copyHeader copies headers, skipping hop-by-hop ones.
func copyHeader(dst, src http.Header) {
	for k, vs := range src {
		switch http.CanonicalHeaderKey(k) {
		case "Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
			"Te", "Trailer", "Transfer-Encoding", "Upgrade":
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// singleJoin joins two URL path segments with exactly one slash.
func singleJoin(a, b string) string {
	switch {
	case a == "":
		return b
	case strings.HasSuffix(a, "/") && strings.HasPrefix(b, "/"):
		return a + b[1:]
	case !strings.HasSuffix(a, "/") && !strings.HasPrefix(b, "/"):
		return a + "/" + b
	default:
		return a + b
	}
}
