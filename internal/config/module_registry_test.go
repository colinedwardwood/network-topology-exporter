package config

import (
	"reflect"
	"testing"
)

// The module-name registry lives in three coordinated places: scopableModules
// here, the moduleGloballyEnabled switch in target_override.go, and the walker
// dispatch table in internal/app/device_walk.go (guarded from the app side by
// TestEnabledModulesMatchScopableModuleNames). These tests pin the two sites in
// this package to each other so adding or renaming a protocol in one place
// fails loudly instead of silently making it un-overridable.

// TestScopableModuleNamesResolveInModuleGloballyEnabled: every canonical
// scopable module name must be recognised by the moduleGloballyEnabled switch.
// A name that falls through to the switch's default-false arm would always
// fail target-override validation with a misleading "not enabled globally"
// error even when the operator enabled it.
func TestScopableModuleNamesResolveInModuleGloballyEnabled(t *testing.T) {
	c := &Config{}
	c.Modules.LLDP.Enabled = true
	c.Modules.CDP.Enabled = true
	c.Modules.FDB.Enabled = true
	c.Modules.OSPF.Enabled = true
	c.Modules.BGP.Enabled = true
	c.Modules.ISIS.Enabled = true
	c.Modules.MPLSTE.Enabled = true

	for _, m := range ScopableModuleNames() {
		if !c.moduleGloballyEnabled(m) {
			t.Errorf("moduleGloballyEnabled(%q) = false with every module toggle enabled; the switch in target_override.go is missing a case for a scopable module", m)
		}
	}
}

// TestModuleGloballyEnabledRejectsUnknown: the default arm must stay false so
// an unrecognised name can never be treated as enabled.
func TestModuleGloballyEnabledRejectsUnknown(t *testing.T) {
	c := &Config{}
	if c.moduleGloballyEnabled("nope") {
		t.Error("moduleGloballyEnabled(\"nope\") = true, want false")
	}
}

// TestScopableModuleNamesSortedAndCanonical pins the exported registry to the
// expected 7-protocol set, sorted. The app-side walker table asserts against
// ScopableModuleNames(), so this is the single place the canonical list is
// spelled out in a test.
func TestScopableModuleNamesSortedAndCanonical(t *testing.T) {
	want := []string{"bgp", "cdp", "fdb", "isis", "lldp", "mpls_te", "ospf"}
	if got := ScopableModuleNames(); !reflect.DeepEqual(got, want) {
		t.Errorf("ScopableModuleNames() = %v, want %v", got, want)
	}
}

// TestUnknownModuleErrorListsCanonicalSet: the validation error for an unknown
// override module must enumerate exactly the canonical scopable set (it is
// derived from scopableModules, not hand-maintained — this test keeps it so).
func TestUnknownModuleErrorListsCanonicalSet(t *testing.T) {
	if got, want := scopableModulesList(), "bgp, cdp, fdb, isis, lldp, mpls_te, ospf"; got != want {
		t.Errorf("scopableModulesList() = %q, want %q", got, want)
	}
}
