package curation

import (
	"testing"

	"github.com/gavinmcfall/containers/apps/licence-gate/internal/identity"
)

func adult() identity.Identity {
	return identity.Identity{User: "alice", Class: identity.ClassFamily, Groups: []string{"family-adult"}}
}
func minor() identity.Identity {
	return identity.Identity{User: "kid", Class: identity.ClassFamily, Groups: []string{"family-minor"}}
}
func brand() identity.Identity {
	return identity.Identity{User: "dopamine-racing", Class: identity.ClassBrand}
}

func sampleSet() *Set {
	return NewSet([]Entry{
		{Path: "workflows/lighthouse/portrait.json", RoleAllowlist: []string{"family-adult", "family-minor"}, Content: []byte(`{"portrait":true}`), Size: 17, ModifiedMS: 1000},
		{Path: "workflows/lighthouse/mature.json", RoleAllowlist: []string{"mature-content"}, Content: []byte(`{"mature":true}`), Size: 15, ModifiedMS: 2000},
		{Path: "workflows/lighthouse/brand-asset.json", RoleAllowlist: []string{"brand:dopamine-racing"}, Content: []byte(`{"brand":true}`), Size: 14, ModifiedMS: 3000},
		{Path: "workflows/everyone.json", RoleAllowlist: nil, Content: []byte(`{"all":true}`), Size: 12, ModifiedMS: 4000},
	})
}

func relPaths(ls []Listed) map[string]Listed {
	m := make(map[string]Listed, len(ls))
	for _, l := range ls {
		m[l.RelPath] = l
	}
	return m
}

func TestListUnder_RelativeToDir_AndRoleFiltered(t *testing.T) {
	s := sampleSet()
	got := relPaths(s.ListUnder("workflows", true, adult()))
	// adult sees: portrait (family-adult), everyone (unrestricted). NOT mature, NOT brand.
	if _, ok := got["lighthouse/portrait.json"]; !ok {
		t.Errorf("adult should see portrait; got %v", got)
	}
	if _, ok := got["everyone.json"]; !ok {
		t.Errorf("adult should see unrestricted entry; got %v", got)
	}
	if _, ok := got["lighthouse/mature.json"]; ok {
		t.Errorf("adult without mature-content must NOT see mature entry")
	}
	if _, ok := got["lighthouse/brand-asset.json"]; ok {
		t.Errorf("family must NOT see brand entry")
	}
	// path is relative to the requested dir ("workflows" stripped).
	if l := got["lighthouse/portrait.json"]; l.Size != 17 || l.ModifiedMS != 1000 {
		t.Errorf("size/modified not carried: %+v", l)
	}
}

func TestListUnder_MinorScoping(t *testing.T) {
	s := sampleSet()
	got := relPaths(s.ListUnder("workflows", true, minor()))
	if _, ok := got["lighthouse/portrait.json"]; !ok {
		t.Errorf("minor should see portrait (family-minor allowed)")
	}
	if _, ok := got["lighthouse/mature.json"]; ok {
		t.Errorf("minor must NEVER see mature-content entry")
	}
}

func TestListUnder_MatureAndBrand(t *testing.T) {
	s := sampleSet()
	// Gavin = family-adult AND mature-content.
	gavin := identity.Identity{User: "gavin", Class: identity.ClassFamily, Groups: []string{"family-adult", "mature-content"}}
	got := relPaths(s.ListUnder("workflows", true, gavin))
	if _, ok := got["lighthouse/mature.json"]; !ok {
		t.Errorf("mature-content holder should see mature entry")
	}
	// brand identity sees brand entry + unrestricted, not family ones.
	gb := relPaths(s.ListUnder("workflows", true, brand()))
	if _, ok := gb["lighthouse/brand-asset.json"]; !ok {
		t.Errorf("brand should see brand-asset")
	}
	if _, ok := gb["everyone.json"]; !ok {
		t.Errorf("brand should see unrestricted entry")
	}
	if _, ok := gb["lighthouse/portrait.json"]; ok {
		t.Errorf("brand must NOT see family-only portrait")
	}
}

func TestListUnder_NonRecurseExcludesNested(t *testing.T) {
	s := sampleSet()
	got := relPaths(s.ListUnder("workflows", false, adult()))
	if _, ok := got["everyone.json"]; !ok {
		t.Errorf("non-recurse should include direct child everyone.json")
	}
	if _, ok := got["lighthouse/portrait.json"]; ok {
		t.Errorf("non-recurse must exclude nested lighthouse/portrait.json")
	}
}

func TestListUnder_DirMismatchExcluded(t *testing.T) {
	s := sampleSet()
	if ls := s.ListUnder("models", true, adult()); len(ls) != 0 {
		t.Errorf("dir=models should match no workflow entries, got %v", ls)
	}
}

func TestLookup_ServesVisibleContent(t *testing.T) {
	s := sampleSet()
	e, ok := s.Lookup("workflows/lighthouse/portrait.json", adult())
	if !ok {
		t.Fatalf("adult should resolve portrait")
	}
	if string(e.Content) != `{"portrait":true}` {
		t.Errorf("wrong content: %s", e.Content)
	}
}

func TestLookup_InvisibleIsNotFound(t *testing.T) {
	s := sampleSet()
	if _, ok := s.Lookup("workflows/lighthouse/mature.json", adult()); ok {
		t.Errorf("adult without mature-content must not resolve mature entry (fail closed)")
	}
	if _, ok := s.Lookup("workflows/does-not-exist.json", adult()); ok {
		t.Errorf("unknown path must not resolve")
	}
}
