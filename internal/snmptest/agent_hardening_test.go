package snmptest

import (
	"testing"

	gsnmp "github.com/gosnmp/gosnmp"
)

// TestHandleBulkClampsLargeMaxReps verifies that handleBulk bounds the number of
// repeater iterations regardless of a huge requested MaxRepetitions, so a crafted
// GetBulk packet cannot drive unbounded sort.Search work.
func TestHandleBulkClampsLargeMaxReps(t *testing.T) {
	// A small contiguous MIB: a walk from before the first OID can return at
	// most len(pdus) values plus one EndOfMibView terminator, so the clamp's
	// effect is observed indirectly via bounded, non-hanging execution.
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.2.2.1.1.1", Type: gsnmp.Integer, Value: 1},
		{Name: ".1.3.6.1.2.1.2.2.1.1.2", Type: gsnmp.Integer, Value: 2},
		{Name: ".1.3.6.1.2.1.2.2.1.1.3", Type: gsnmp.Integer, Value: 3},
	}

	result := handleBulk(pdus, []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.2.2.1.1"},
	}, 0, 1_000_000)

	// One repeater over 3 entries yields at most maxBulkRepetitions varbinds;
	// here the walk terminates naturally at 3 values + 1 EndOfMibView = 4.
	if len(result) > maxBulkRepetitions+1 {
		t.Fatalf("handleBulk returned %d varbinds, want <= %d", len(result), maxBulkRepetitions+1)
	}
	if len(result) != 4 {
		t.Errorf("handleBulk returned %d varbinds, want 4 (3 values + EndOfMibView)", len(result))
	}
}

// TestHandleBulkClampWithRepeatingMib verifies the clamp is the binding limit
// when the MIB is large enough that an unbounded walk would otherwise emit far
// more than the cap. We build maxBulkRepetitions+50 contiguous OIDs so a single
// repeater with a huge MaxRepetitions is bounded by the cap, not the MIB size.
func TestHandleBulkClampWithRepeatingMib(t *testing.T) {
	n := maxBulkRepetitions + 50
	pdus := make([]gsnmp.SnmpPDU, 0, n)
	for i := 1; i <= n; i++ {
		pdus = append(pdus, gsnmp.SnmpPDU{
			Name:  ".1.3.6.1.2.1.2.2.1.1." + itoa(i),
			Type:  gsnmp.Integer,
			Value: i,
		})
	}
	// handleBulk relies on sorted input; build them in numeric order then sort
	// with the same comparator the agent uses.
	sortPDUs(pdus)

	result := handleBulk(pdus, []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.2.2.1.1.0"},
	}, 0, 1_000_000)

	if len(result) != maxBulkRepetitions {
		t.Errorf("handleBulk returned %d varbinds, want exactly the cap %d", len(result), maxBulkRepetitions)
	}
}

// TestHandleBulkNegativeMaxReps verifies a negative MaxRepetitions (possible from
// a crafted packet) is clamped rather than producing zero or unbounded work.
func TestHandleBulkNegativeMaxReps(t *testing.T) {
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Type: gsnmp.OctetString, Value: []byte("descr")},
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte("name")},
	}
	result := handleBulk(pdus, []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0"},
	}, 0, -5)
	// Negative is clamped to the cap, so the walk proceeds normally and is bounded.
	if len(result) == 0 {
		t.Error("handleBulk with negative maxReps: expected bounded non-empty result")
	}
	if len(result) > maxBulkRepetitions+1 {
		t.Errorf("handleBulk negative maxReps returned %d varbinds, want <= %d", len(result), maxBulkRepetitions+1)
	}
}

// TestNextComponentOverflow verifies nextComponent does not panic or wrap
// negative on an oversized numeric component, and clamps to the sentinel.
func TestNextComponentOverflow(t *testing.T) {
	// A component far longer than int can hold (60 nines).
	huge := "999999999999999999999999999999999999999999999999999999999999"
	v, next := nextComponent(huge, 0)
	if v < 0 {
		t.Errorf("nextComponent overflow value = %d, want non-negative (no wrap)", v)
	}
	if v != componentOverflow {
		t.Errorf("nextComponent overflow value = %d, want clamp to %d", v, componentOverflow)
	}
	if next != len(huge) {
		t.Errorf("nextComponent overflow next = %d, want %d (consumed whole component)", next, len(huge))
	}

	// Overflow followed by a dot and another component: parsing must continue.
	v2, next2 := nextComponent(huge+".7", 0)
	if v2 != componentOverflow {
		t.Errorf("nextComponent overflow-with-dot value = %d, want %d", v2, componentOverflow)
	}
	v3, _ := nextComponent(huge+".7", next2)
	if v3 != 7 {
		t.Errorf("nextComponent after overflow component = %d, want 7", v3)
	}
}

// TestOidLessOverflowDeterministic verifies oidLess produces a deterministic,
// non-panicking ordering when components overflow. Two overflowing components
// both clamp to the sentinel, so they compare equal and ordering falls through
// to the remaining components.
func TestOidLessOverflowDeterministic(t *testing.T) {
	huge := "1." + repeat9(40)
	bigger := "1." + repeat9(45)

	// Both second components clamp to componentOverflow → equal → ordering is
	// decided by length (a is a prefix-equal of itself here). Must not panic.
	if oidLess(huge, huge) {
		t.Error("oidLess(huge, huge) = true, want false (equal)")
	}
	// Clamped-equal second component, then differing trailing components.
	a := huge + ".1"
	b := bigger + ".2"
	got := oidLess(a, b)
	// Whatever the result, it must be the deterministic inverse of the swap.
	if got == oidLess(b, a) {
		t.Errorf("oidLess not antisymmetric for overflow OIDs: oidLess(a,b)=%v oidLess(b,a)=%v", got, oidLess(b, a))
	}
}

// TestNextComponentNonDigit verifies non-digit bytes are skipped safely and do
// not panic or poison the accumulated value.
func TestNextComponentNonDigit(t *testing.T) {
	// A component containing stray non-digit bytes: digits still accumulate.
	v, _ := nextComponent("1a2", 0)
	if v != 12 {
		t.Errorf("nextComponent(\"1a2\") = %d, want 12 (non-digit skipped)", v)
	}
	// Purely non-digit component yields 0 and advances past it without panic.
	v2, next2 := nextComponent("xyz.3", 0)
	if v2 != 0 {
		t.Errorf("nextComponent(\"xyz.3\") value = %d, want 0", v2)
	}
	if next2 != 4 {
		t.Errorf("nextComponent(\"xyz.3\") next = %d, want 4", next2)
	}
}

// itoa is a tiny base-10 int formatter to avoid importing strconv in the test
// alongside the existing package style.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}

// repeat9 returns a string of n '9' characters.
func repeat9(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = '9'
	}
	return string(b)
}

// sortPDUs sorts in place using the same comparator the agent uses.
func sortPDUs(pdus []gsnmp.SnmpPDU) {
	// Insertion-style sort via the package comparator keeps the test free of
	// extra imports while matching oidLess ordering.
	for i := 1; i < len(pdus); i++ {
		for j := i; j > 0 && oidLess(pdus[j].Name, pdus[j-1].Name); j-- {
			pdus[j], pdus[j-1] = pdus[j-1], pdus[j]
		}
	}
}
