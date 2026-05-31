package audit

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Per ADR 016: render audit is a structured log stream — one JSON object per
// line on stdout — composed from the identity half (user, identity-class, raw
// personal flag, resolved commercial + rationale) and the gate half (model,
// licence, decision), plus a timestamp. The cluster log aggregator picks it up.
func TestWriteEmitsOneJSONLinePerRecord(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)

	rec := Record{
		Timestamp:             "2026-06-01T08:30:00Z",
		User:                  "gavin",
		IdentityClass:         "brand",
		PersonalFlagRequested: true,
		Commercial:            true,
		CommercialRationale:   "brand identity is always commercial; personal flag ignored",
		Model:                 "flux1-dev-fp8.safetensors",
		Licence:               "FLUX.1 dev NC",
		Decision:              "reject",
		PromptID:              "p-123",
	}
	if err := w.Write(rec); err != nil {
		t.Fatalf("Write: %v", err)
	}

	out := buf.String()
	if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
		t.Fatalf("want exactly one trailing newline, got %q", out)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("audit line is not valid JSON: %v\nline: %s", err, out)
	}
	// §6 record completeness: both halves + timestamp present.
	for _, k := range []string{"ts", "user", "identity_class", "personal_flag_requested", "commercial", "commercial_rationale", "model", "licence", "decision", "prompt_id"} {
		if _, ok := got[k]; !ok {
			t.Errorf("audit record missing field %q", k)
		}
	}
	if got["user"] != "gavin" || got["decision"] != "reject" || got["commercial"] != true {
		t.Errorf("unexpected field values: %v", got)
	}
}

// Two writes produce two independent lines (append, not overwrite).
func TestWriteAppendsLines(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf)
	_ = w.Write(Record{Timestamp: "t1", User: "alice", Decision: "allow"})
	_ = w.Write(Record{Timestamp: "t2", User: "bob", Decision: "allow"})
	if n := strings.Count(buf.String(), "\n"); n != 2 {
		t.Fatalf("want 2 lines, got %d: %q", n, buf.String())
	}
}
