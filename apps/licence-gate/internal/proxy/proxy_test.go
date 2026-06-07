package proxy

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gavinmcfall/containers/apps/licence-gate/internal/audit"
	"github.com/gavinmcfall/containers/apps/licence-gate/internal/isolation"
	"github.com/gavinmcfall/containers/apps/licence-gate/internal/registry"
	"github.com/gavinmcfall/containers/apps/licence-gate/internal/tier"
	"github.com/gavinmcfall/containers/apps/licence-gate/internal/workflow"
)

// fakeComfy is a stand-in ComfyUI master. It records what the proxy forwarded so
// tests can assert the request was gated/rewritten before it reached upstream.
type fakeComfy struct {
	mu             sync.Mutex
	promptCalls         int
	lastPromptBody      []byte
	lastPromptOrigin    string
	distributedCalls    int
	lastDistributedBody []byte
	viewCalls           int
	historyJSON         string
	queueJSON           string
	jobsJSON            string
	// view: filenames that "exist"; anything else → upstream 404.
	existingViews map[string]bool
	// userdata capture: paths (EscapedPath) and queries master saw on the
	// /userdata routes, plus call counts. Filled by handler() below.
	userdataCalls       int
	lastUserdataMethod  string
	lastUserdataRawPath string
	lastUserdataQuery   string
	// userdataListJSON/Status let a test drive the /userdata listing response the
	// master returns (default `["fake"]`/200), e.g. a full_info array or a 404 for
	// a fresh user whose workflows dir doesn't exist yet.
	userdataListJSON   string
	userdataListStatus int
}

func (f *fakeComfy) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/prompt", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.promptCalls++
		f.lastPromptBody = body
		f.lastPromptOrigin = r.Header.Get("Origin")
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"prompt_id":"pid-123","number":1,"node_errors":{}}`)
	})
	mux.HandleFunc("/view", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.viewCalls++
		f.mu.Unlock()
		key := r.URL.Query().Get("subfolder") + "/" + r.URL.Query().Get("filename")
		if f.existingViews[key] {
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte("PNGDATA"))
			return
		}
		// ComfyUI's own 404 body — deliberately DIFFERENT from the proxy's
		// canonical 404, so the test proves the proxy normalises it.
		w.WriteHeader(http.StatusNotFound)
		io.WriteString(w, `{"comfy":"file not found, distinct body"}`)
	})
	mux.HandleFunc("/distributed/queue", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.distributedCalls++
		f.lastDistributedBody = body
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"prompt_id":"pid-dq","worker_count":2,"auto_prepare_supported":true}`)
	})
	mux.HandleFunc("/history", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, f.historyJSON)
	})
	mux.HandleFunc("/queue", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, f.queueJSON)
	})
	mux.HandleFunc("/jobs", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, f.jobsJSON)
	})
	// /userdata catch-all — captures what master sees so tests can assert the
	// proxy injected the user bucket AND preserved %2F encoding through forward.
	mux.HandleFunc("/userdata", func(w http.ResponseWriter, r *http.Request) {
		f.captureUserdata(r)
		w.Header().Set("Content-Type", "application/json")
		if f.userdataListStatus != 0 {
			w.WriteHeader(f.userdataListStatus)
		}
		body := f.userdataListJSON
		if body == "" {
			body = `["fake"]`
		}
		io.WriteString(w, body)
	})
	mux.HandleFunc("/userdata/", func(w http.ResponseWriter, r *http.Request) {
		f.captureUserdata(r)
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, "ok")
	})
	// Mirror real master: every route is also served under /api (server.py
	// registers "/api"+route.path for all routes). Strip a leading /api segment
	// before dispatching so the fake answers /api/prompt, /api/view, /api/userdata,
	// … exactly as master does. r.URL.RawPath is preserved for the userdata capture.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api" {
			r.URL.Path = "/"
			r.URL.RawPath = ""
		} else if strings.HasPrefix(r.URL.Path, "/api/") {
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api")
			if r.URL.RawPath != "" {
				r.URL.RawPath = strings.TrimPrefix(r.URL.RawPath, "/api")
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func (f *fakeComfy) captureUserdata(r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.userdataCalls++
	f.lastUserdataMethod = r.Method
	f.lastUserdataRawPath = r.URL.EscapedPath()
	f.lastUserdataQuery = r.URL.RawQuery
}

// makeJWT builds a decode-only Pocket-ID-style id_token (bogus signature; the
// proxy only decodes claims — Envoy verifies upstream).
func makeJWT(sub string, groups ...string) string {
	b64 := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	hdr := b64(map[string]any{"alg": "RS256", "typ": "JWT"})
	payload := b64(map[string]any{"sub": sub, "groups": groups})
	return hdr + "." + payload + ".sig"
}

// promptBody builds a ComfyUI POST /prompt envelope around a minimal SDXL graph.
func promptBody(ckpt, prefix, clientID string) []byte {
	env := map[string]any{
		"client_id": clientID,
		"prompt": map[string]any{
			"1": map[string]any{"class_type": "CheckpointLoaderSimple", "inputs": map[string]any{"ckpt_name": ckpt}},
			"2": map[string]any{"class_type": "CLIPTextEncode", "inputs": map[string]any{"text": "a cat", "clip": []any{"1", 1}}},
			"3": map[string]any{"class_type": "EmptyLatentImage", "inputs": map[string]any{"width": 1024, "height": 1024}},
			"4": map[string]any{"class_type": "KSampler", "inputs": map[string]any{"seed": 12345, "model": []any{"1", 0}}},
			"5": map[string]any{"class_type": "VAEDecode", "inputs": map[string]any{"samples": []any{"4", 0}}},
			"6": map[string]any{"class_type": "SaveImage", "inputs": map[string]any{"filename_prefix": prefix, "images": []any{"5", 0}}},
		},
	}
	b, _ := json.Marshal(env)
	return b
}

// newTestProxy wires a proxy in front of fc with the given registry entries.
func newTestProxy(t *testing.T, fc *fakeComfy, entries []registry.Entry) (*Proxy, *fakeComfy, *bytes.Buffer, *isolation.PromptOwners) {
	return newTestProxyWithTiers(t, fc, entries, nil)
}

func newTestProxyWithTiers(t *testing.T, fc *fakeComfy, entries []registry.Entry, tiers tier.Map) (*Proxy, *fakeComfy, *bytes.Buffer, *isolation.PromptOwners) {
	t.Helper()
	upstream := httptest.NewServer(fc.handler())
	t.Cleanup(upstream.Close)
	u, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	regJSON, _ := json.Marshal(entries)
	reg, err := registry.Load(regJSON)
	if err != nil {
		t.Fatal(err)
	}
	auditBuf := &bytes.Buffer{}
	owners := isolation.NewPromptOwners(128)
	p := New(Config{
		Upstream:   u,
		Allowlist:  workflow.DefaultAllowlist,
		Registry:   reg,
		BrandToken: "brand-secret",
		Audit:      audit.NewWriter(auditBuf),
		Owners:      owners,
		Now:         func() string { return "2026-06-01T00:00:00Z" },
		WorkerTiers: tiers,
	})
	return p, fc, auditBuf, owners
}

func do(p *Proxy, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)
	return rec
}

func auditLines(t *testing.T, buf *bytes.Buffer) []audit.Record {
	t.Helper()
	var recs []audit.Record
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var r audit.Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("bad audit line %q: %v", line, err)
		}
		recs = append(recs, r)
	}
	return recs
}

func TestPromptAllowedForwardsRewrittenAndRemembers(t *testing.T) {
	fc := &fakeComfy{}
	p, _, auditBuf, owners := newTestProxy(t, fc, []registry.Entry{
		{Filename: "sdxl_base.safetensors", Licence: "OpenRAIL++", CommercialOK: true},
	})

	req := httptest.NewRequest(http.MethodPost, "/prompt", bytes.NewReader(promptBody("sdxl_base.safetensors", "render", "c1")))
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin", "family-adult"))
	rec := do(p, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "pid-123") {
		t.Errorf("response should carry upstream prompt_id, got %s", rec.Body.String())
	}
	if fc.promptCalls != 1 {
		t.Fatalf("upstream prompt calls = %d, want 1", fc.promptCalls)
	}
	// The forwarded body must preserve client_id AND scope SaveImage to gavin/.
	var fwd struct {
		ClientID string                   `json:"client_id"`
		Prompt   map[string]workflow.Node `json:"prompt"`
	}
	if err := json.Unmarshal(fc.lastPromptBody, &fwd); err != nil {
		t.Fatalf("forwarded body not valid: %v", err)
	}
	if fwd.ClientID != "c1" {
		t.Errorf("client_id = %q, want c1 (envelope must be preserved)", fwd.ClientID)
	}
	if got := fwd.Prompt["6"].Inputs["filename_prefix"]; got != "gavin/render" {
		t.Errorf("filename_prefix = %v, want gavin/render", got)
	}
	if u, known := owners.Owner("pid-123"); !known || u != "gavin" {
		t.Errorf("owner of pid-123 = %q known=%v, want gavin/true", u, known)
	}
	recs := auditLines(t, auditBuf)
	if len(recs) != 1 || recs[0].Decision != "allow" || recs[0].User != "gavin" || recs[0].Commercial != true {
		t.Errorf("audit = %+v, want one allow record for gavin commercial", recs)
	}
}

func TestPromptRejectsNonCommercialWithSubstitutes(t *testing.T) {
	fc := &fakeComfy{}
	p, _, auditBuf, _ := newTestProxy(t, fc, []registry.Entry{
		{Filename: "anime_nc.safetensors", Licence: "CreativeML-NC", CommercialOK: false, Substitutes: []string{"sdxl_base.safetensors"}},
	})

	req := httptest.NewRequest(http.MethodPost, "/prompt", bytes.NewReader(promptBody("anime_nc.safetensors", "render", "c1")))
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin", "family-adult"))
	rec := do(p, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if fc.promptCalls != 0 {
		t.Errorf("rejected job must NOT reach upstream, calls=%d", fc.promptCalls)
	}
	var resp struct {
		Violations []struct {
			Filename    string   `json:"filename"`
			Reason      string   `json:"reason"`
			Substitutes []string `json:"substitutes"`
		} `json:"violations"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("403 body not JSON: %v (%s)", err, rec.Body.String())
	}
	if len(resp.Violations) != 1 || resp.Violations[0].Filename != "anime_nc.safetensors" ||
		len(resp.Violations[0].Substitutes) != 1 || resp.Violations[0].Substitutes[0] != "sdxl_base.safetensors" {
		t.Errorf("violations = %+v, want anime_nc with sdxl_base substitute", resp.Violations)
	}
	if recs := auditLines(t, auditBuf); len(recs) != 1 || recs[0].Decision != "reject" {
		t.Errorf("audit = %+v, want one reject record", recs)
	}
}

func TestPromptPersonalFlagAllowsNonCommercial(t *testing.T) {
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, []registry.Entry{
		{Filename: "anime_nc.safetensors", CommercialOK: false},
	})
	req := httptest.NewRequest(http.MethodPost, "/prompt", bytes.NewReader(promptBody("anime_nc.safetensors", "render", "c1")))
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin", "family-adult"))
	req.Header.Set(HeaderPersonal, "true")
	rec := do(p, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("personal job with NC model should pass: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if fc.promptCalls != 1 {
		t.Errorf("personal job must reach upstream, calls=%d", fc.promptCalls)
	}
}

func TestPromptUnknownNodeRejected(t *testing.T) {
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, nil)
	env := map[string]any{"prompt": map[string]any{
		"1": map[string]any{"class_type": "EvilCustomSaver", "inputs": map[string]any{}},
	}}
	b, _ := json.Marshal(env)
	req := httptest.NewRequest(http.MethodPost, "/prompt", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin"))
	rec := do(p, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown node → status=%d, want 400", rec.Code)
	}
	if fc.promptCalls != 0 {
		t.Errorf("invalid workflow must NOT reach upstream, calls=%d", fc.promptCalls)
	}
}

func TestPromptUnsafePrefixRejected(t *testing.T) {
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, []registry.Entry{{Filename: "sdxl_base.safetensors", CommercialOK: true}})
	req := httptest.NewRequest(http.MethodPost, "/prompt", bytes.NewReader(promptBody("sdxl_base.safetensors", "../../escape", "c1")))
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin"))
	rec := do(p, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unsafe prefix → status=%d, want 400", rec.Code)
	}
	if fc.promptCalls != 0 {
		t.Errorf("unsafe prefix must NOT reach upstream, calls=%d", fc.promptCalls)
	}
}

func TestPromptMissingAuthRejected(t *testing.T) {
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, nil)
	req := httptest.NewRequest(http.MethodPost, "/prompt", bytes.NewReader(promptBody("x.safetensors", "r", "c1")))
	rec := do(p, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing auth → status=%d, want 401", rec.Code)
	}
	if fc.promptCalls != 0 {
		t.Errorf("unauthenticated must NOT reach upstream, calls=%d", fc.promptCalls)
	}
}

func TestViewOwnBucketForwarded(t *testing.T) {
	fc := &fakeComfy{existingViews: map[string]bool{"gavin/render_00001_.png": true}}
	p, _, _, _ := newTestProxy(t, fc, nil)
	req := httptest.NewRequest(http.MethodGet, "/view?filename=render_00001_.png&subfolder=gavin&type=output", nil)
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin"))
	rec := do(p, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "PNGDATA" {
		t.Fatalf("own-bucket view should stream image: status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestViewOtherBucketDeniedIdenticalToGenuine404(t *testing.T) {
	fc := &fakeComfy{existingViews: map[string]bool{}} // nothing exists
	p, _, _, _ := newTestProxy(t, fc, nil)

	// Denied: gavin asks for alice's bucket. Must NOT hit upstream.
	denyReq := httptest.NewRequest(http.MethodGet, "/view?filename=secret.png&subfolder=alice&type=output", nil)
	denyReq.Header.Set("Authorization", "Bearer "+makeJWT("gavin"))
	deny := do(p, denyReq)
	if fc.viewCalls != 0 {
		t.Errorf("denied view must not reach upstream, calls=%d", fc.viewCalls)
	}

	// Genuine 404: gavin asks for a missing file in his OWN bucket → upstream 404.
	missReq := httptest.NewRequest(http.MethodGet, "/view?filename=missing.png&subfolder=gavin&type=output", nil)
	missReq.Header.Set("Authorization", "Bearer "+makeJWT("gavin"))
	miss := do(p, missReq)

	if deny.Code != http.StatusNotFound || miss.Code != http.StatusNotFound {
		t.Fatalf("both must be 404: deny=%d miss=%d", deny.Code, miss.Code)
	}
	if deny.Body.String() != miss.Body.String() {
		t.Errorf("denied vs genuine-404 bodies differ (existence side-channel):\n deny=%q\n miss=%q", deny.Body.String(), miss.Body.String())
	}
	if deny.Header().Get("Content-Type") != miss.Header().Get("Content-Type") {
		t.Errorf("denied vs genuine-404 content-type differ: %q vs %q", deny.Header().Get("Content-Type"), miss.Header().Get("Content-Type"))
	}
	// Must NOT leak ComfyUI's distinct body.
	if strings.Contains(miss.Body.String(), "distinct body") {
		t.Errorf("genuine 404 leaked upstream body: %q", miss.Body.String())
	}
}

func TestViewNonOutputTypeDenied(t *testing.T) {
	fc := &fakeComfy{existingViews: map[string]bool{"gavin/x.png": true}}
	p, _, _, _ := newTestProxy(t, fc, nil)
	req := httptest.NewRequest(http.MethodGet, "/view?filename=x.png&subfolder=gavin&type=input", nil)
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin"))
	rec := do(p, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("non-output view type must be denied as 404, got %d", rec.Code)
	}
}

func TestHistoryFilteredToOwner(t *testing.T) {
	fc := &fakeComfy{
		historyJSON: `{"pid-mine":{"x":1},"pid-other":{"x":2},"pid-unknown":{"x":3}}`,
	}
	p, _, _, owners := newTestProxy(t, fc, nil)
	owners.Remember("pid-mine", "gavin")
	owners.Remember("pid-other", "alice")

	req := httptest.NewRequest(http.MethodGet, "/history", nil)
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin"))
	rec := do(p, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("history status=%d", rec.Code)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("history body: %v", err)
	}
	if _, ok := got["pid-mine"]; !ok {
		t.Errorf("own history entry dropped: %v", got)
	}
	if _, ok := got["pid-other"]; ok {
		t.Errorf("other user's history leaked: %v", got)
	}
	if _, ok := got["pid-unknown"]; ok {
		t.Errorf("unknown-owner history leaked: %v", got)
	}
}

func TestQueueFilteredToOwner(t *testing.T) {
	fc := &fakeComfy{
		queueJSON: `{"queue_running":[[0,"pid-mine",{},{},[]]],"queue_pending":[[1,"pid-other",{},{},[]]]}`,
	}
	p, _, _, owners := newTestProxy(t, fc, nil)
	owners.Remember("pid-mine", "gavin")
	owners.Remember("pid-other", "alice")

	req := httptest.NewRequest(http.MethodGet, "/queue", nil)
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin"))
	rec := do(p, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("queue status=%d", rec.Code)
	}
	var got struct {
		Running [][]json.RawMessage `json:"queue_running"`
		Pending [][]json.RawMessage `json:"queue_pending"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("queue body: %v (%s)", err, rec.Body.String())
	}
	if len(got.Running) != 1 {
		t.Errorf("own running entry dropped: %+v", got.Running)
	}
	if len(got.Pending) != 0 {
		t.Errorf("other user's pending entry leaked: %+v", got.Pending)
	}
}

// TestJobsScopedToOwner: GET /api/jobs must return only the caller's own jobs,
// scoped by preview_output.subfolder (the per-user output bucket == the caller's
// id). Another user's completed job must never leak into the list.
func TestJobsScopedToOwner(t *testing.T) {
	fc := &fakeComfy{
		jobsJSON: `{"jobs":[` +
			`{"id":"j-mine","status":"completed","execution_start_time":1780731331770,"execution_end_time":1780731361040,"preview_output":{"filename":"a.png","subfolder":"gavin","type":"output"}},` +
			`{"id":"j-other","status":"completed","execution_start_time":1780731000000,"execution_end_time":1780731009999,"preview_output":{"filename":"secret.png","subfolder":"alice","type":"output"}}` +
			`],"pagination":{"offset":0,"limit":200,"total":2,"has_more":false}}`,
	}
	p, _, _, _ := newTestProxy(t, fc, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/jobs?status=completed,failed,cancelled&limit=200&offset=0", nil)
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin"))
	rec := do(p, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("jobs status=%d", rec.Code)
	}
	var got struct {
		Jobs []map[string]json.RawMessage `json:"jobs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("jobs body: %v (%s)", err, rec.Body.String())
	}
	if len(got.Jobs) != 1 {
		t.Fatalf("expected 1 own job, got %d (other user's job leaked?): %s", len(got.Jobs), rec.Body.String())
	}
	var id string
	_ = json.Unmarshal(got.Jobs[0]["id"], &id)
	if id != "j-mine" {
		t.Errorf("wrong job returned: %q", id)
	}
}

// TestJobsInjectCreateTime: the frontend's Zod schema requires create_time:number,
// but ComfyUI's /api/jobs response omits it (only execution_start_time/end_time).
// The proxy must inject create_time (from execution_start_time) so the panel parses.
func TestJobsInjectCreateTime(t *testing.T) {
	fc := &fakeComfy{
		jobsJSON: `{"jobs":[` +
			`{"id":"j-mine","status":"completed","execution_start_time":1780731331770,"execution_end_time":1780731361040,"preview_output":{"filename":"a.png","subfolder":"gavin","type":"output"}}` +
			`],"pagination":{"offset":0,"limit":200,"total":1,"has_more":false}}`,
	}
	p, _, _, _ := newTestProxy(t, fc, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/jobs?status=completed&limit=200&offset=0", nil)
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin"))
	rec := do(p, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("jobs status=%d", rec.Code)
	}
	var got struct {
		Jobs []map[string]json.RawMessage `json:"jobs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("jobs body: %v", err)
	}
	if len(got.Jobs) != 1 {
		t.Fatalf("expected 1 job, got %d", len(got.Jobs))
	}
	ct, ok := got.Jobs[0]["create_time"]
	if !ok {
		t.Fatalf("create_time not injected (frontend Zod needs it): %s", rec.Body.String())
	}
	var ctn float64
	if err := json.Unmarshal(ct, &ctn); err != nil {
		t.Errorf("create_time not a number: %s", string(ct))
	}
	if ctn != 1780731331770 {
		t.Errorf("create_time=%v, want execution_start_time 1780731331770", ctn)
	}
}

// TestJobsCompletedFromDiskDurableAndScoped: completed jobs must be served from the
// persistent /output/<user>/ dir (durable — survives a master restart that wiped the
// in-memory history), scoped to the caller, with create_time. Other users' buckets
// must never leak.
func TestJobsCompletedFromDiskDurableAndScoped(t *testing.T) {
	tmp := t.TempDir()
	for _, d := range []string{"gavin", "alice"} {
		if err := os.MkdirAll(filepath.Join(tmp, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"a_00001_.png", "b_00001_.png"} {
		os.WriteFile(filepath.Join(tmp, "gavin", f), []byte("PNG"), 0o644)
	}
	os.WriteFile(filepath.Join(tmp, "alice", "secret_00001_.png"), []byte("PNG"), 0o644)

	// Master's in-memory history is EMPTY (as after a restart) — the durable list
	// must still return the on-disk renders.
	fc := &fakeComfy{jobsJSON: `{"jobs":[],"pagination":{"total":0}}`}
	p, _, _, _ := newTestProxy(t, fc, nil)
	p.cfg.OutputDir = tmp

	req := httptest.NewRequest(http.MethodGet, "/api/jobs?status=completed,failed,cancelled&limit=200&offset=0", nil)
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin"))
	rec := do(p, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("jobs status=%d", rec.Code)
	}
	var got struct {
		Jobs []map[string]json.RawMessage `json:"jobs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("jobs body: %v (%s)", err, rec.Body.String())
	}
	if len(got.Jobs) != 2 {
		t.Fatalf("expected 2 durable jobs from disk, got %d: %s", len(got.Jobs), rec.Body.String())
	}
	for _, j := range got.Jobs {
		var po map[string]json.RawMessage
		_ = json.Unmarshal(j["preview_output"], &po)
		var subfolder string
		_ = json.Unmarshal(po["subfolder"], &subfolder)
		if subfolder != "gavin" {
			t.Errorf("leaked non-gavin subfolder: %q", subfolder)
		}
		// zPreviewOutput.nodeId is REQUIRED (z.string()) — without it the frontend
		// rejects the whole jobs list and the panel renders empty.
		if _, ok := po["nodeId"]; !ok {
			t.Errorf("preview_output missing nodeId (frontend Zod requires it): %s", j["preview_output"])
		}
		if _, ok := j["create_time"]; !ok {
			t.Errorf("durable job missing create_time: %s", j)
		}
		var st string
		_ = json.Unmarshal(j["status"], &st)
		if st != "completed" {
			t.Errorf("durable job status=%q want completed", st)
		}
	}
}

// distributedBody wraps the same SDXL graph in a /distributed/queue envelope
// (the ComfyUI-Distributed GPU render path) — extra fields beyond /prompt.
func distributedBody(ckpt, prefix, clientID string) []byte {
	var env map[string]any
	_ = json.Unmarshal(promptBody(ckpt, prefix, clientID), &env)
	env["enabled_worker_ids"] = []string{"gpu-vengeance", "gpu-vixen"}
	env["delegate_master"] = true
	b, _ := json.Marshal(env)
	return b
}

// The GPU render path (/distributed/queue) MUST be gated exactly like /prompt —
// otherwise a render bypasses the licence gate by using the distributed endpoint.
func TestDistributedQueueRejectsNonCommercial(t *testing.T) {
	fc := &fakeComfy{}
	p, _, auditBuf, _ := newTestProxy(t, fc, []registry.Entry{
		{Filename: "anime_nc.safetensors", CommercialOK: false, Substitutes: []string{"sdxl_base.safetensors"}},
	})
	req := httptest.NewRequest(http.MethodPost, "/distributed/queue", bytes.NewReader(distributedBody("anime_nc.safetensors", "render", "c1")))
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin", "family-adult"))
	rec := do(p, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (distributed path must be gated)", rec.Code)
	}
	if fc.distributedCalls != 0 {
		t.Errorf("rejected distributed job must NOT reach upstream, calls=%d", fc.distributedCalls)
	}
	if recs := auditLines(t, auditBuf); len(recs) != 1 || recs[0].Decision != "reject" {
		t.Errorf("audit = %+v, want one reject record", recs)
	}
}

func TestDistributedQueueAllowedRewritesAndRemembers(t *testing.T) {
	fc := &fakeComfy{}
	p, _, _, owners := newTestProxy(t, fc, []registry.Entry{
		{Filename: "sdxl_base.safetensors", CommercialOK: true},
	})
	req := httptest.NewRequest(http.MethodPost, "/distributed/queue", bytes.NewReader(distributedBody("sdxl_base.safetensors", "render", "c1")))
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin", "family-adult"))
	rec := do(p, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if fc.distributedCalls != 1 {
		t.Fatalf("upstream distributed calls = %d, want 1", fc.distributedCalls)
	}
	// Output scoped to the user, AND the distributed-only fields preserved.
	var fwd struct {
		ClientID         string                   `json:"client_id"`
		EnabledWorkerIDs []string                 `json:"enabled_worker_ids"`
		DelegateMaster   bool                     `json:"delegate_master"`
		Prompt           map[string]workflow.Node `json:"prompt"`
	}
	if err := json.Unmarshal(fc.lastDistributedBody, &fwd); err != nil {
		t.Fatalf("forwarded body invalid: %v", err)
	}
	if got := fwd.Prompt["6"].Inputs["filename_prefix"]; got != "gavin/render" {
		t.Errorf("filename_prefix = %v, want gavin/render", got)
	}
	if len(fwd.EnabledWorkerIDs) != 2 || !fwd.DelegateMaster {
		t.Errorf("distributed envelope fields not preserved: %+v", fwd)
	}
	if u, known := owners.Owner("pid-dq"); !known || u != "gavin" {
		t.Errorf("owner of pid-dq = %q known=%v, want gavin/true", u, known)
	}
}

// TestForwardStripsOriginHeader verifies that the proxy strips the Origin
// header before forwarding to upstream. Required because ComfyUI master has
// built-in DNS-rebinding protection that returns 403 when the browser-set
// Origin doesn't match the upstream Host (which the proxy rewrites to
// 127.0.0.1:8188 on forward). Without this strip, every browser-originated
// request fails with HTTP 403 + master log "request with non matching host
// and origin ..., returning 403". Discovered Lighthouse smoke 2026-06-03.
func TestForwardStripsOriginHeader(t *testing.T) {
	fc := &fakeComfy{}
	p, fc, _, _ := newTestProxy(t, fc, []registry.Entry{
		{Filename: "sdxl_base.safetensors", CommercialOK: true, Licence: "OK"},
	})
	req := httptest.NewRequest(http.MethodPost, "/prompt", bytes.NewReader(promptBody("sdxl_base.safetensors", "x", "c1")))
	req.Header.Set("Authorization", "Bearer "+makeJWT("user-a"))
	// Simulate the browser-set Origin that would otherwise reach master and trip
	// its DNS-rebinding defense.
	req.Header.Set("Origin", "https://lighthouse.nerdz.cloud")
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK from forwarded prompt, got %d (body=%s)", rec.Code, rec.Body.String())
	}
	if fc.promptCalls != 1 {
		t.Fatalf("expected upstream /prompt called once, got %d", fc.promptCalls)
	}
	if fc.lastPromptOrigin != "" {
		t.Errorf("Origin should be STRIPPED before upstream; got %q (would trigger master 403)", fc.lastPromptOrigin)
	}
}

// Compile-time assertion that the helper signature stays in sync.
var _ = fmt.Sprintf

// ──────────────────────────────────────────────────────────────────────────
// /userdata scoping (Plan 1b — v0.1.3)
// ──────────────────────────────────────────────────────────────────────────

func TestUserdataMissingAuthRejected(t *testing.T) {
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, nil)
	req := httptest.NewRequest(http.MethodGet, "/userdata/foo.json", nil)
	// no Authorization header
	rec := do(p, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing auth should 401, got %d", rec.Code)
	}
	if fc.userdataCalls != 0 {
		t.Errorf("denied userdata must not reach upstream, calls=%d", fc.userdataCalls)
	}
}

func TestUserdataSingleFileInjectsUserBucket(t *testing.T) {
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, nil)
	req := httptest.NewRequest(http.MethodGet, "/userdata/test.json", nil)
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin"))
	rec := do(p, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%q", rec.Code, rec.Body.String())
	}
	want := "/userdata/gavin%2Ftest.json"
	if fc.lastUserdataRawPath != want {
		t.Errorf("upstream saw EscapedPath=%q, want %q", fc.lastUserdataRawPath, want)
	}
}

func TestUserdataPreservesEncodedSlashThroughForward(t *testing.T) {
	// The headline bug — Phase-1 used the default reverse proxy which decoded
	// %2F → /, master saw /userdata/<user>/workflows/foo.json (multi-segment) and
	// returned 405 on POST (route is /userdata/{file}, single segment). The fix
	// is forwardEncoded preserving RawPath.
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, nil)
	req := httptest.NewRequest(http.MethodPost, "/userdata/workflows%2FTest%20Flow.json?overwrite=true",
		strings.NewReader(`{"x":1}`))
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin"))
	req.Header.Set("Content-Type", "application/json")
	rec := do(p, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%q", rec.Code, rec.Body.String())
	}
	want := "/userdata/gavin%2Fworkflows%2FTest%20Flow.json"
	if fc.lastUserdataRawPath != want {
		t.Errorf("upstream saw EscapedPath=%q, want %q (encoding decoded somewhere in the chain)",
			fc.lastUserdataRawPath, want)
	}
	if fc.lastUserdataQuery != "overwrite=true" {
		t.Errorf("query not preserved: %q", fc.lastUserdataQuery)
	}
	if fc.lastUserdataMethod != http.MethodPost {
		t.Errorf("method not preserved: %q", fc.lastUserdataMethod)
	}
}

func TestUserdataEnvoyDecodedSlashStillScopes(t *testing.T) {
	// The PRODUCTION case: Envoy Gateway runs UNESCAPE_AND_REDIRECT, so the
	// browser's POST /userdata/workflows%2FTest%20Flow.json arrives at this proxy
	// already decoded to a LITERAL slash: /userdata/workflows/Test%20Flow.json.
	// httptest.NewRequest reproduces that shape (Path decoded, RawPath partial).
	// The rewrite must still hand master ONE %2F-segment or master 405s on save.
	// Lighthouse Plan-1b 2026-06-04 — this is what the v0.1.3 encoded-only code missed.
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, nil)
	req := httptest.NewRequest(http.MethodPost, "/userdata/workflows/Test%20Flow.json?overwrite=true",
		strings.NewReader(`{"x":1}`))
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin"))
	req.Header.Set("Content-Type", "application/json")
	rec := do(p, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d body=%q", rec.Code, rec.Body.String())
	}
	want := "/userdata/gavin%2Fworkflows%2FTest%20Flow.json"
	if fc.lastUserdataRawPath != want {
		t.Errorf("Envoy-decoded inbound: upstream saw EscapedPath=%q, want %q", fc.lastUserdataRawPath, want)
	}
}

// ── /api prefix routing (Lighthouse Plan-1b HAR 2026-06-04) ──
// The web frontend prepends /api to every native call. Matching only the bare
// path let EVERY browser request fall through to passthrough — bypassing the
// licence gate (/api/prompt) and per-user isolation (/api/userdata, /api/view).
// These tests pin that the gated handlers fire on the /api spelling too.

func TestUserdataApiPrefixScopes(t *testing.T) {
	// The exact 405 case from the HAR: browser POST to /api/userdata/<%2F-path>.
	// Must reach handleUserdata (not passthrough) and inject the caller's bucket.
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/userdata/workflows%2FTest%20Realvisxl.json?overwrite=false",
		strings.NewReader(`{"x":1}`))
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin"))
	req.Header.Set("Content-Type", "application/json")
	rec := do(p, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/userdata save status = %d body=%q", rec.Code, rec.Body.String())
	}
	// fakeComfy strips /api before capture (mirrors master); the bucket prefix is
	// what proves handleUserdata fired rather than passthrough.
	if !strings.Contains(fc.lastUserdataRawPath, "gavin%2Fworkflows%2FTest%20Realvisxl.json") {
		t.Errorf("/api/userdata not scoped — handler bypassed? upstream saw %q", fc.lastUserdataRawPath)
	}
}

func TestPromptApiPrefixGated(t *testing.T) {
	// /api/prompt must hit the licence gate. A non-commercial model on a brand
	// (service) identity is rejected — proving the gate ran on the /api spelling.
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, []registry.Entry{
		{Filename: "anime_nc.safetensors", Licence: "CreativeML-NC", CommercialOK: false},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/prompt", bytes.NewReader(promptBody("anime_nc.safetensors", "render", "c1")))
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin", "family-adult"))
	rec := do(p, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("/api/prompt non-commercial status = %d, want 403 (gate must fire on /api)", rec.Code)
	}
	if fc.promptCalls != 0 {
		t.Errorf("rejected /api/prompt must NOT reach upstream, calls=%d", fc.promptCalls)
	}
}

func TestViewApiPrefixScoped(t *testing.T) {
	// /api/view for another user's bucket must be denied with the canonical 404,
	// proving view-scoping fires on the /api spelling (else cross-user image read).
	fc := &fakeComfy{existingViews: map[string]bool{}}
	p, _, _, _ := newTestProxy(t, fc, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/view?filename=secret.png&subfolder=alice&type=output", nil)
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin"))
	rec := do(p, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("/api/view cross-user status = %d, want 404", rec.Code)
	}
	if fc.viewCalls != 0 {
		t.Errorf("denied /api/view must NOT reach upstream, calls=%d", fc.viewCalls)
	}
}

func TestUserdataListingScopesDirQuery(t *testing.T) {
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, nil)
	req := httptest.NewRequest(http.MethodGet, "/userdata?dir=workflows", nil)
	req.Header.Set("Authorization", "Bearer "+makeJWT("alice"))
	rec := do(p, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	q, _ := url.ParseQuery(fc.lastUserdataQuery)
	if q.Get("dir") != "alice/workflows" {
		t.Errorf("upstream saw dir=%q, want alice/workflows", q.Get("dir"))
	}
}

func TestUserdataListingRootDefaultsToUserBucket(t *testing.T) {
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, nil)
	req := httptest.NewRequest(http.MethodGet, "/userdata", nil)
	req.Header.Set("Authorization", "Bearer "+makeJWT("alice"))
	_ = do(p, req)
	q, _ := url.ParseQuery(fc.lastUserdataQuery)
	if q.Get("dir") != "alice" {
		t.Errorf("upstream saw dir=%q, want alice", q.Get("dir"))
	}
}

func TestUserdataMoveEndpointScopesBothFileAndDest(t *testing.T) {
	// Master's route is POST /userdata/{file}/move/{dest}. If only file got
	// scoped, user could move their file OUT of their bucket via dest.
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, nil)
	req := httptest.NewRequest(http.MethodPost,
		"/userdata/workflows%2Fa.json/move/workflows%2Fb.json", nil)
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin"))
	rec := do(p, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unexpected status: %d", rec.Code)
	}
	want := "/userdata/gavin%2Fworkflows%2Fa.json/move/gavin%2Fworkflows%2Fb.json"
	if fc.lastUserdataRawPath != want {
		t.Errorf("upstream saw EscapedPath=%q, want %q", fc.lastUserdataRawPath, want)
	}
}

func TestUserdataTraversalRejectedBeforeForward(t *testing.T) {
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, nil)
	req := httptest.NewRequest(http.MethodGet, "/userdata/..%2Fetc%2Fpasswd", nil)
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin"))
	rec := do(p, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("traversal should 400, got %d", rec.Code)
	}
	if fc.userdataCalls != 0 {
		t.Errorf("unsafe userdata must not reach upstream, calls=%d", fc.userdataCalls)
	}
}

func TestUserdataTwoUsersGetDisjointBuckets(t *testing.T) {
	// Same logical filename from two callers must produce different upstream
	// paths — proves the per-user namespace is per-caller, not shared.
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, nil)

	reqA := httptest.NewRequest(http.MethodGet, "/userdata/workflows%2Fshared.json", nil)
	reqA.Header.Set("Authorization", "Bearer "+makeJWT("alice"))
	_ = do(p, reqA)
	pathAlice := fc.lastUserdataRawPath

	reqB := httptest.NewRequest(http.MethodGet, "/userdata/workflows%2Fshared.json", nil)
	reqB.Header.Set("Authorization", "Bearer "+makeJWT("bob"))
	_ = do(p, reqB)
	pathBob := fc.lastUserdataRawPath

	if pathAlice == pathBob {
		t.Errorf("two callers got the same upstream path %q — bucket isolation broken", pathAlice)
	}
	if !strings.Contains(pathAlice, "alice%2F") || !strings.Contains(pathBob, "bob%2F") {
		t.Errorf("each caller's bucket should be in their path: alice=%q bob=%q", pathAlice, pathBob)
	}
}

func TestPromptRejectsMatureModelForNonMatureCaller(t *testing.T) {
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, []registry.Entry{
		{Filename: "sd_xl_base_1.0.safetensors", CommercialOK: true, RequiresGroup: "mature-content"},
	})
	req := httptest.NewRequest(http.MethodPost, "/prompt",
		bytes.NewReader(promptBody("sd_xl_base_1.0.safetensors", "render", "c1")))
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin", "family-adult"))
	req.Header.Set("X-Lighthouse-Personal", "true") // personal so licence passes; group must still block
	rec := do(p, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (mature model, no group)", rec.Code)
	}
	if fc.promptCalls != 0 {
		t.Errorf("rejected job must not reach upstream, calls=%d", fc.promptCalls)
	}
}

func TestPromptAllowsMatureModelForMatureCaller(t *testing.T) {
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, []registry.Entry{
		{Filename: "sd_xl_base_1.0.safetensors", CommercialOK: true, RequiresGroup: "mature-content"},
	})
	req := httptest.NewRequest(http.MethodPost, "/prompt",
		bytes.NewReader(promptBody("sd_xl_base_1.0.safetensors", "render", "c1")))
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin", "family-adult", "mature-content"))
	req.Header.Set("X-Lighthouse-Personal", "true")
	rec := do(p, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (caller has mature-content)", rec.Code)
	}
}

// distributedBodyWorkers builds a /distributed/queue envelope with a specific
// enabled_worker_ids list (overriding the default in distributedBody).
func distributedBodyWorkers(ckpt, prefix, clientID string, workerIDs []string) []byte {
	var env map[string]any
	_ = json.Unmarshal(distributedBody(ckpt, prefix, clientID), &env)
	env["enabled_worker_ids"] = workerIDs
	b, _ := json.Marshal(env)
	return b
}

// lastDistributedWorkerIDs reads enabled_worker_ids from the last captured envelope.
func (f *fakeComfy) lastDistributedWorkerIDs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var env struct {
		IDs []string `json:"enabled_worker_ids"`
	}
	_ = json.Unmarshal(f.lastDistributedBody, &env)
	return env.IDs
}

func TestDistributedQueueDropsIncapableWorker(t *testing.T) {
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxyWithTiers(t, fc,
		[]registry.Entry{{Filename: "sd_xl_base_1.0.safetensors", CommercialOK: true, TierFit: []string{"heavy"}}},
		tier.Map{"venge": "heavy", "nova": "light"})
	env := distributedBodyWorkers("sd_xl_base_1.0.safetensors", "render", "c1", []string{"venge", "nova"})
	req := httptest.NewRequest(http.MethodPost, "/distributed/queue", bytes.NewReader(env))
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin", "family-adult"))
	req.Header.Set("X-Lighthouse-Personal", "true")
	rec := do(p, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	ids := fc.lastDistributedWorkerIDs()
	if len(ids) != 1 || ids[0] != "venge" {
		t.Errorf("upstream enabled_worker_ids = %v, want [venge] (nova dropped)", ids)
	}
}

func TestDistributedQueueRejectsWhenNoCapableWorker(t *testing.T) {
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxyWithTiers(t, fc,
		[]registry.Entry{{Filename: "sd_xl_base_1.0.safetensors", CommercialOK: true, TierFit: []string{"heavy"}}},
		tier.Map{"nova": "light"})
	env := distributedBodyWorkers("sd_xl_base_1.0.safetensors", "render", "c1", []string{"nova"})
	req := httptest.NewRequest(http.MethodPost, "/distributed/queue", bytes.NewReader(env))
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin", "family-adult"))
	req.Header.Set("X-Lighthouse-Personal", "true")
	rec := do(p, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 (no capable worker)", rec.Code)
	}
	if fc.distributedCalls != 0 {
		t.Errorf("must not reach upstream, calls=%d", fc.distributedCalls)
	}
}
