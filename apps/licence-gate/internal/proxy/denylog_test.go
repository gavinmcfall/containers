package proxy

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Denials must be visible in the gate's own log: today a 403/401 is silent
// (http.Error only), so debugging "the button did nothing" means correlating
// through the forward-auth proxy's access log. One line per gate denial with
// method, path, status, and caller closes that gap.
func TestDeniedRequestsAreLogged(t *testing.T) {
	tmp := outputTree(t)
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, nil)
	p.cfg.OutputDir = tmp
	var buf bytes.Buffer
	p.cfg.Logger = log.New(&buf, "", 0)

	// Cross-user delete → 403, and the denial is logged with the essentials.
	rec := postHistoryDelete(p, makeJWT("gavin"), `{"delete":["`+syntheticJobID("alice", "secret_00001_.png")+`"]}`)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want 403", rec.Code)
	}
	line := buf.String()
	for _, want := range []string{"403", "POST", "/api/history", "gavin"} {
		if !strings.Contains(line, want) {
			t.Errorf("deny log missing %q: %q", want, line)
		}
	}

	// Missing auth → 401 logged with user "-".
	buf.Reset()
	req := httptest.NewRequest(http.MethodPost, "/api/history", bytes.NewBufferString(`{"delete":["x"]}`))
	do(p, req)
	if !strings.Contains(buf.String(), "401") || !strings.Contains(buf.String(), "user=-") {
		t.Errorf("401 deny log wrong: %q", buf.String())
	}
}

func TestAllowedRequestsAreNotLogged(t *testing.T) {
	tmp := outputTree(t)
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, nil)
	p.cfg.OutputDir = tmp
	var buf bytes.Buffer
	p.cfg.Logger = log.New(&buf, "", 0)

	rec := postHistoryDelete(p, makeJWT("gavin"), `{"delete":["`+syntheticJobID("gavin", "a_00001_.png")+`"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	if buf.Len() != 0 {
		t.Errorf("successful request logged: %q", buf.String())
	}
}

// The synthesized gallery's canonical 404s (/view deny-or-missing ambiguity) are
// designed noise — they stay out of the deny log.
func TestCanonical404NotLogged(t *testing.T) {
	fc := &fakeComfy{}
	p, _, _, _ := newTestProxy(t, fc, nil)
	var buf bytes.Buffer
	p.cfg.Logger = log.New(&buf, "", 0)

	req := httptest.NewRequest(http.MethodGet, "/view?filename=nope.png&subfolder=gavin&type=output", nil)
	req.Header.Set("Authorization", "Bearer "+makeJWT("gavin"))
	rec := do(p, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", rec.Code)
	}
	if buf.Len() != 0 {
		t.Errorf("canonical 404 logged: %q", buf.String())
	}
}

// The status-capturing wrapper must not break hijacking (websocket passthrough)
// or flushing (streamed responses).
func TestStatusWriterPreservesOptionalInterfaces(t *testing.T) {
	rec := httptest.NewRecorder() // implements Flusher, not Hijacker
	sw := &statusWriter{ResponseWriter: rec}
	if _, ok := interface{}(sw).(http.Flusher); !ok {
		t.Error("statusWriter lost Flusher")
	}
	if _, _, err := sw.Hijack(); err == nil {
		t.Error("Hijack on non-hijackable writer must error, not panic")
	}
}
