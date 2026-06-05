// Package tier filters distributed-render worker selection by VRAM tier so a job
// is never fanned out to a worker that cannot run its models (e.g. SDXL to a
// 3050, which would OOM). Acceptable tiers = the intersection of every referenced
// model's tier_fit; a worker is kept only if its tier is acceptable. Unknown or
// untagged models do not constrain — the licence gate owns unknown-model policy
// (plan 2026-06-05-role-tier-gating).
package tier

import (
	"fmt"

	"github.com/gavinmcfall/containers/apps/licence-gate/internal/registry"
	"github.com/gavinmcfall/containers/apps/licence-gate/internal/workflow"
)

// Map is worker_id -> tier (e.g. "heavy", "light").
type Map map[string]string

// Filter returns the subset of requested worker IDs that can run the job, in the
// original order. It errors if the job has a tier constraint but no requested
// worker satisfies it (the caller should surface this as a 400, not a silent
// empty fan-out).
func Filter(refs []workflow.ModelRef, reg *registry.Registry, workers Map, requested []string) ([]string, error) {
	acceptable := acceptableTiers(refs, reg) // nil => unconstrained
	if acceptable == nil {
		return requested, nil
	}
	var kept []string
	for _, id := range requested {
		if acceptable[workers[id]] {
			kept = append(kept, id)
		}
	}
	if len(kept) == 0 {
		return nil, fmt.Errorf("no enabled worker can run this job's models (required tiers: %v)", keys(acceptable))
	}
	return kept, nil
}

// acceptableTiers intersects tier_fit across all known, tagged refs. Returns nil
// when nothing constrains (no refs, or every ref is untagged/unknown).
func acceptableTiers(refs []workflow.ModelRef, reg *registry.Registry) map[string]bool {
	var acc map[string]bool
	for _, ref := range refs {
		e, ok := reg.Resolve(ref.Filename)
		if !ok || len(e.TierFit) == 0 {
			continue
		}
		set := map[string]bool{}
		for _, t := range e.TierFit {
			set[t] = true
		}
		if acc == nil {
			acc = set
			continue
		}
		for t := range acc {
			if !set[t] {
				delete(acc, t)
			}
		}
	}
	return acc
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
