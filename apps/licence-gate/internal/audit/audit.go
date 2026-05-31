// Package audit emits the per-render audit record as a structured log stream:
// one JSON object per line, to stdout (ADR 016). The record is composed from the
// identity half (user, identity-class, raw personal flag, resolved commercial +
// rationale) and the gate half (model, licence, decision) so neither the gate
// nor the identity layer needs to know about the other (image-generation §6).
// The cluster log aggregator ingests these; a CNPG generation_audit table
// replaces stdout when Plan 4 lands.
package audit

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// Record is one §6 audit entry. The caller stamps Timestamp (RFC3339) so the
// writer stays clock-free and unit-testable.
type Record struct {
	Timestamp             string `json:"ts"`
	User                  string `json:"user"`
	IdentityClass         string `json:"identity_class"`
	PersonalFlagRequested bool   `json:"personal_flag_requested"`
	Commercial            bool   `json:"commercial"`
	CommercialRationale   string `json:"commercial_rationale"`
	Model                 string `json:"model"`
	Licence               string `json:"licence"`
	Decision              string `json:"decision"`
	PromptID              string `json:"prompt_id"`
}

// Writer serialises audit records to an io.Writer (stdout in production), one
// JSON line each. Safe for concurrent use by the proxy's request handlers.
type Writer struct {
	mu  sync.Mutex
	enc *json.Encoder // json.Encoder.Encode appends a newline per value
}

// NewWriter returns an audit Writer over w.
func NewWriter(w io.Writer) *Writer {
	return &Writer{enc: json.NewEncoder(w)}
}

// Write emits one record as a single JSON line.
func (w *Writer) Write(rec Record) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.enc.Encode(rec); err != nil {
		return fmt.Errorf("write audit record: %w", err)
	}
	return nil
}
