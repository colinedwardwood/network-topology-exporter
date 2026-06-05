package snmp

import (
	"testing"

	"go.uber.org/goleak"
)

// TestMain runs the snmp package tests under goleak so a leaked goroutine or
// socket fails the suite. The session pool (issue #83) runs a background
// evictLoop goroutine and holds UDP sessions; goleak is the guard that a
// Close()d pool stops its evictor and releases every session, and that the
// pooled-session tests do not leak gosnmp sockets. No IgnoreFunction is
// registered: every goroutine this package starts (the evictor, the in-process
// snmptest agent's serve loop) is stopped deterministically before the suite
// exits, so any survivor is a real leak we want to catch.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}
