package app

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs the app package tests under goleak so a leaked goroutine fails
// the suite. This package owns the discovery loop/cycle worker goroutines and
// the opt-in pprof debug server (issue #69); goleak is the guard that a clean
// shutdown of those paths leaves nothing running. No IgnoreFunction is
// registered: every goroutine the package starts is expected to stop on its
// own teardown, so any survivor is a real leak we want to catch.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
