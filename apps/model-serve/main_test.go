package main

import "testing"

func TestEnvOr(t *testing.T) {
	t.Setenv("MODELSERVE_TEST_KEY", "v")
	if got := envOr("MODELSERVE_TEST_KEY", "fb"); got != "v" {
		t.Errorf("set = %q, want v", got)
	}
	if got := envOr("MODELSERVE_UNSET_XYZ", "fb"); got != "fb" {
		t.Errorf("unset = %q, want fb", got)
	}
}
