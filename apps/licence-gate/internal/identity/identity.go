// Package identity translates a Pocket-ID claim (delivered via the Envoy-injected
// Authorization: Bearer JWT) plus the per-request personal flag into the resolved
// licence intent the gate consumes. It owns "commercial" resolution so the gate
// stays policy-pure and licence-agnostic (ADR-reviewed seam, 2026-05-30).
package identity

// Class is the identity class derived from the Pocket-ID group/claim.
type Class string

const (
	ClassBrand   Class = "brand"   // Dopamine Racing / brand identity — always commercial
	ClassFamily  Class = "family"  // a family member — may flag a job personal
	ClassService Class = "service" // a service account — always commercial
)

// ResolveCommercial applies commercial-by-default: every job is commercial unless
// a family identity explicitly flags it personal. A brand or service identity is
// always commercial (a personal flag from them is ignored). Returns the resolved
// intent and a rationale recorded in the §6 audit trail.
func ResolveCommercial(class Class, personalFlag bool) (commercial bool, rationale string) {
	switch class {
	case ClassBrand:
		return true, "brand identity is always commercial; personal flag ignored"
	case ClassService:
		return true, "service account is always commercial"
	case ClassFamily:
		if personalFlag {
			return false, "family identity flagged the job personal"
		}
		return true, "commercial by default (no personal flag)"
	default:
		// Unknown class fails safe to commercial (most-restrictive model set).
		return true, "unknown identity class; commercial by default (fail-safe)"
	}
}
