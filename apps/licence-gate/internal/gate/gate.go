// Package gate applies the licence policy: commercial-by-default. Every job is
// commercial (and thus restricted to commercial-safe models) unless explicitly
// flagged personal — forgetting the flag fails SAFE, never into a licence
// violation (image-generation-and-licensing.md §5).
package gate

import (
	"github.com/gavinmcfall/containers/apps/licence-gate/internal/registry"
	"github.com/gavinmcfall/containers/apps/licence-gate/internal/workflow"
)

// Decision actions.
const (
	ActionAllow  = "allow"
	ActionReject = "reject"
)

// Violation reasons.
const (
	ReasonNonCommercial = "non-commercial"
	ReasonUnknown       = "unknown"
)

// Job carries the resolved licence intent for a submission. Commercial is the
// already-resolved intent (commercial-by-default + brand-always-commercial are
// applied upstream when reading identity).
type Job struct {
	Commercial bool
}

// Violation is one model that fails the policy.
type Violation struct {
	Filename    string
	Reason      string
	Substitutes []string
}

// Decision is the gate outcome for a job.
type Decision struct {
	Allowed    bool
	Action     string
	Violations []Violation
}

// Evaluate resolves each referenced model against the registry and applies the
// commercial-by-default policy. A commercial job may use only commercial-OK,
// known models; a personal job may use any known model (and unknown models,
// since the unknown=unsafe block is a commercial-only conservatism).
func Evaluate(refs []workflow.ModelRef, reg *registry.Registry, job Job) Decision {
	var violations []Violation
	for _, ref := range refs {
		entry, known := reg.Resolve(ref.Filename)
		switch {
		case !known:
			if job.Commercial {
				violations = append(violations, Violation{
					Filename: ref.Filename,
					Reason:   ReasonUnknown,
				})
			}
		case job.Commercial && !entry.CommercialOK:
			violations = append(violations, Violation{
				Filename:    ref.Filename,
				Reason:      ReasonNonCommercial,
				Substitutes: entry.Substitutes,
			})
		}
	}

	if len(violations) > 0 {
		return Decision{Allowed: false, Action: ActionReject, Violations: violations}
	}
	return Decision{Allowed: true, Action: ActionAllow}
}
