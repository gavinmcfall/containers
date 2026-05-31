package identity

import "testing"

// commercial-by-default (image-generation-and-licensing.md §5): every job is
// commercial unless a non-brand identity explicitly flags it personal.
// Fixtures confirmed by Chat A's contract review (2026-05-30).
func TestResolveCommercial(t *testing.T) {
	cases := []struct {
		name         string
		class        Class
		personalFlag bool
		want         bool
	}{
		// A brand identity is ALWAYS commercial — a personal flag from it is ignored.
		{"brand ignores personal flag", ClassBrand, true, true},
		// A family identity may flag personal to unlock non-commercial models.
		{"family honours personal flag", ClassFamily, true, false},
		// A service account is always commercial.
		{"service account always commercial", ClassService, true, true},
		// Missing/absent personal flag → commercial by default (fail-safe).
		{"family default is commercial", ClassFamily, false, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, rationale := ResolveCommercial(tc.class, tc.personalFlag)
			if got != tc.want {
				t.Errorf("ResolveCommercial(%q, %v) = %v, want %v", tc.class, tc.personalFlag, got, tc.want)
			}
			if rationale == "" {
				t.Error("rationale must be recorded for the audit trail")
			}
		})
	}
}
