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
	"strings"
	"sync"
	"testing"

	"github.com/gavinmcfall/containers/apps/licence-gate/internal/audit"
	"github.com/gavinmcfall/containers/apps/licence-gate/internal/isolation"
	"github.com/gavinmcfall/containers/apps/licence-gate/internal/registry"
	"github.com/gavinmcfall/containers/apps/licence-gate/internal/workflow"
)

// fakeComfy is a stand-in ComfyUI master. It records what the proxy forwarded so
// tests can assert the request was gated/rewritten before it reached upstream.
type fakeComfy struct {
	mu             sync.Mutex
	promptCalls    int
	lastPromptBody []byte
	viewCalls      int
	historyJSON    string
	queueJSON      string
	// view: filenames that "exist"; anything else → upstream 404.
	existingViews map[string]bool
}

func (f *fakeComfy) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/prompt", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.promptCalls++
		f.lastPromptBody = body
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
	mux.HandleFunc("/history", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, f.historyJSON)
	})
	mux.HandleFunc("/queue", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, f.queueJSON)
	})
	return mux
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
		Owners:     owners,
		Now:        func() string { return "2026-06-01T00:00:00Z" },
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

// Compile-time assertion that the helper signature stays in sync.
var _ = fmt.Sprintf
