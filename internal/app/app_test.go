package app

import "testing"

// TestPackageHasTestTarget exists because go test -coverprofile across
// `./...` requires every instrumented package to have at least one test
// target; otherwise the auto-downloaded toolchain on Go 1.24 CI hits
// "go: no such tool covdata" when merging profiles for a package with
// only production code. The substantive tests for this package live in
// cmd/topology-exporter/main_test.go and exercise the exported surface
// via the `app.` prefix; this file just keeps the coverage merge happy.
//
// TODO: a follow-up should relocate the helper-level tests from
// main_test.go (TestDeduplicateOOS et al.) into this package for cohesion.
func TestPackageHasTestTarget(t *testing.T) {
	t.Parallel()
}
