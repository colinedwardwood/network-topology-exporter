package app

import "testing"

// TestPackageHasTestTarget exists because go test -coverprofile across
// `./...` requires every instrumented package to have at least one test
// target; otherwise the auto-downloaded toolchain on Go 1.24 CI hits
// "go: no such tool covdata" when merging profiles for a package with
// only production code. The behavioural tests for this package live in the
// sibling *_test.go files (package app for whitebox tests; package app_test
// for the exported-surface tests relocated from main_test.go in #171); this
// file just keeps the coverage merge happy.
func TestPackageHasTestTarget(t *testing.T) {
	t.Parallel()
}
