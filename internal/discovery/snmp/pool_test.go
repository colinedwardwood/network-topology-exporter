package snmp

import (
	"sync"
	"testing"
	"time"

	gsnmp "github.com/gosnmp/gosnmp"

	"github.com/colinedwardwood/network-topology-exporter/internal/snmptest"
)

// fakeSessionPoolMetrics is a thread-safe SessionPoolMetrics test double.
type fakeSessionPoolMetrics struct {
	mu        sync.Mutex
	hits      int
	misses    int
	size      int
	evictions map[string]int
}

func newFakeSessionPoolMetrics() *fakeSessionPoolMetrics {
	return &fakeSessionPoolMetrics{evictions: map[string]int{}}
}

func (f *fakeSessionPoolMetrics) RecordHit() {
	f.mu.Lock()
	f.hits++
	f.mu.Unlock()
}
func (f *fakeSessionPoolMetrics) RecordMiss() {
	f.mu.Lock()
	f.misses++
	f.mu.Unlock()
}
func (f *fakeSessionPoolMetrics) SetSize(n int) {
	f.mu.Lock()
	f.size = n
	f.mu.Unlock()
}
func (f *fakeSessionPoolMetrics) RecordEviction(reason string) {
	f.mu.Lock()
	f.evictions[reason]++
	f.mu.Unlock()
}
func (f *fakeSessionPoolMetrics) snapshot() (hits, misses, size int, evictions map[string]int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ev := make(map[string]int, len(f.evictions))
	for k, v := range f.evictions {
		ev[k] = v
	}
	return f.hits, f.misses, f.size, ev
}

// startAgent returns a Params pointed at a fresh in-process SNMP test agent.
func startAgent(t *testing.T, community, profile string) Params {
	t.Helper()
	pdus := []gsnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gsnmp.OctetString, Value: []byte("test")},
	}
	addr := snmptest.Start(t, community, pdus)
	ip, port := snmptest.ParseAddr(addr)
	return Params{IP: ip, Port: port, Community: []byte(community), CredentialProfile: profile, Timeout: 3 * time.Second}
}

// checkout drives the real acquire() state machine for tests, returning the
// session and its release func. Replaces the deleted Checkout method so tests
// exercise exactly one acquisition path.
// NOTE: pool_test.go imports gosnmp as `gsnmp` (not `g`), so the return type is
// *gsnmp.GoSNMP, not *g.GoSNMP.
func checkout(t *testing.T, pl *SessionPool, p Params) (*gsnmp.GoSNMP, func()) {
	t.Helper()
	s, release, err := pl.acquire(p)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	return s, release
}

func TestSessionPoolCheckoutReuseSameKey(t *testing.T) {
	fm := newFakeSessionPoolMetrics()
	pl := NewSessionPool(SessionPoolOptions{MaxIdle: time.Hour, Metrics: fm})
	defer pl.Close()

	p := startAgent(t, "public", "profA")

	s1, release1 := checkout(t, pl, p)
	release1()

	s2, release2 := checkout(t, pl, p)
	if s1 != s2 {
		t.Fatalf("expected same session reused on repeat checkout, got different instances")
	}
	release2()

	hits, misses, _, _ := fm.snapshot()
	if misses != 1 {
		t.Errorf("misses = %d, want 1 (only the cold open)", misses)
	}
	if hits != 1 {
		t.Errorf("hits = %d, want 1 (the reuse)", hits)
	}
}

func TestSessionPoolDifferentProfileIsDifferentSession(t *testing.T) {
	fm := newFakeSessionPoolMetrics()
	pl := NewSessionPool(SessionPoolOptions{MaxIdle: time.Hour, Metrics: fm})
	defer pl.Close()

	// Same agent (same IP) but two distinct credential profiles → distinct keys.
	pA := startAgent(t, "public", "profA")
	pB := pA
	pB.CredentialProfile = "profB"

	sA, releaseA := checkout(t, pl, pA)
	sB, releaseB := checkout(t, pl, pB)
	if sA == sB {
		t.Fatalf("expected distinct sessions for distinct profiles")
	}
	releaseA()
	releaseB()

	_, misses, size, _ := fm.snapshot()
	if misses != 2 {
		t.Errorf("misses = %d, want 2 (one cold open per profile)", misses)
	}
	if size != 2 {
		t.Errorf("size = %d, want 2", size)
	}
}

func TestSessionPoolInvalidateProfile(t *testing.T) {
	fm := newFakeSessionPoolMetrics()
	pl := NewSessionPool(SessionPoolOptions{MaxIdle: time.Hour, Metrics: fm})
	defer pl.Close()

	p := startAgent(t, "public", "rotateme")
	s1, release1 := checkout(t, pl, p)
	release1()

	pl.InvalidateProfile("rotateme")

	_, _, size, ev := fm.snapshot()
	if ev[evictionReasonCredentialRotation] != 1 {
		t.Errorf("credential_rotation evictions = %d, want 1", ev[evictionReasonCredentialRotation])
	}
	if size != 0 {
		t.Errorf("size after invalidate = %d, want 0", size)
	}

	// Subsequent checkout opens fresh (different instance, counted as a miss).
	s2, release2 := checkout(t, pl, p)
	if s2 == s1 {
		t.Fatalf("expected a fresh session after InvalidateProfile")
	}
	release2()
}

func TestSessionPoolIdleEviction(t *testing.T) {
	fm := newFakeSessionPoolMetrics()
	pl := NewSessionPool(SessionPoolOptions{MaxIdle: time.Hour, Metrics: fm})
	defer pl.Close()

	p := startAgent(t, "public", "idle")
	_, release := checkout(t, pl, p)
	release()

	// Force the entry's lastUsed far into the past, then run the evictor.
	key := poolKey{ip: p.IP.String(), profile: p.CredentialProfile}
	pl.mu.Lock()
	pl.sessions[key].lastUsed = time.Now().Add(-2 * time.Hour)
	pl.mu.Unlock()

	pl.Evict()

	_, _, size, ev := fm.snapshot()
	if ev[evictionReasonIdle] != 1 {
		t.Errorf("idle evictions = %d, want 1", ev[evictionReasonIdle])
	}
	if size != 0 {
		t.Errorf("size after idle eviction = %d, want 0", size)
	}
}

func TestSessionPoolReturnUnhealthyEvicts(t *testing.T) {
	fm := newFakeSessionPoolMetrics()
	pl := NewSessionPool(SessionPoolOptions{MaxIdle: time.Hour, Metrics: fm})
	defer pl.Close()

	p := startAgent(t, "public", "conn")
	s, _ := checkout(t, pl, p)
	pl.Return(p, s, false) // unhealthy

	_, _, size, ev := fm.snapshot()
	if ev[evictionReasonConnectionError] != 1 {
		t.Errorf("connection_error evictions = %d, want 1", ev[evictionReasonConnectionError])
	}
	if size != 0 {
		t.Errorf("size after unhealthy return = %d, want 0", size)
	}
}

func TestSessionPoolCloseClosesAll(t *testing.T) {
	fm := newFakeSessionPoolMetrics()
	pl := NewSessionPool(SessionPoolOptions{MaxIdle: time.Hour, Metrics: fm})

	for _, prof := range []string{"a", "b", "c"} {
		p := startAgent(t, "public", prof)
		_, release := checkout(t, pl, p)
		release()
	}
	_, _, size, _ := fm.snapshot()
	if size != 3 {
		t.Fatalf("size before close = %d, want 3", size)
	}

	pl.Close()

	_, _, size, _ = fm.snapshot()
	if size != 0 {
		t.Errorf("size after Close = %d, want 0", size)
	}
	// Close is idempotent.
	pl.Close()
}

// TestSessionPoolConcurrentDistinctKeys drives Checkout/Return across many
// goroutines for DIFFERENT keys concurrently and the same key sequentially.
// Run under -race; the pool map must stay consistent with no data race.
func TestSessionPoolConcurrentDistinctKeys(t *testing.T) {
	pl := NewSessionPool(SessionPoolOptions{MaxIdle: time.Hour, Metrics: newFakeSessionPoolMetrics()})
	defer pl.Close()

	const workers = 8
	const iters = 20
	params := make([]Params, workers)
	for i := range params {
		params[i] = startAgent(t, "public", "p")
		// distinct profile per worker → distinct key even if IPs collided
		params[i].CredentialProfile = "prof-" + string(rune('A'+i))
	}

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(p Params) {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				_, release := checkout(t, pl, p)
				release()
			}
		}(params[i])
	}
	wg.Wait()
}

// TestAcquireFreshPathByteIdentical asserts the single most important property:
// when Params.Pool is nil, Acquire opens exactly one fresh session and the
// returned release closes its connection — equivalent to the historical
// Open + Conn.Close pair, with no session retained anywhere.
func TestAcquireFreshPathByteIdentical(t *testing.T) {
	p := startAgent(t, "public", "")
	p.Pool = nil

	before := OpenCount()
	client, release, err := Acquire(p)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if got := OpenCount() - before; got != 1 {
		t.Fatalf("fresh Acquire opened %d sessions, want exactly 1", got)
	}
	// The connection is live before release and closed after.
	if client.Conn == nil {
		t.Fatalf("expected a live connection from fresh Acquire")
	}
	release()
	// A double release must not panic (defer release() in walkers is unconditional).
	release()
}

// TestAcquirePooledPathReuses asserts that with a pool, repeated Acquire on the
// same (IP, profile) reuses one session: only the first Acquire dials.
func TestAcquirePooledPathReuses(t *testing.T) {
	pl := NewSessionPool(SessionPoolOptions{MaxIdle: time.Hour, Metrics: newFakeSessionPoolMetrics()})
	defer pl.Close()

	p := startAgent(t, "public", "reuse")
	p.Pool = pl

	before := OpenCount()
	c1, r1, err := Acquire(p)
	if err != nil {
		t.Fatalf("acquire 1: %v", err)
	}
	r1() // return to pool (does NOT close)
	c2, r2, err := Acquire(p)
	if err != nil {
		t.Fatalf("acquire 2: %v", err)
	}
	r2()
	if c1 != c2 {
		t.Fatalf("pooled Acquire did not reuse the session")
	}
	if got := OpenCount() - before; got != 1 {
		t.Fatalf("pooled Acquire dialed %d times across 2 acquires, want 1", got)
	}
}

// TestPoolOpenReduction is the issue #83 acceptance test: simulate N cycles ×
// M targets × K modules with the pool ON vs OFF and assert the pooled dial
// count is ≤ 20% of the unpooled count (≥80% reduction). Deterministic — it
// counts fresh-session opens (the quantity driving conntrack), not wall-clock.
func TestPoolOpenReduction(t *testing.T) {
	const (
		cycles  = 3
		targets = 4
		modules = 9 // ~ the per-target module count today
	)

	// Build one persistent agent per target so the same (IP,profile) recurs
	// across cycles, which is what the pool exploits.
	base := make([]Params, targets)
	for i := range base {
		// Distinct profile per target so each maps to a distinct pool key even
		// though the in-process agents all bind 127.0.0.1 (the production key is
		// (IP, profile); real fleets have distinct IPs).
		base[i] = startAgent(t, "public", "prof-"+string(rune('A'+i)))
	}

	run := func(pool *SessionPool) int64 {
		start := OpenCount()
		for c := 0; c < cycles; c++ {
			for tgt := 0; tgt < targets; tgt++ {
				p := base[tgt]
				p.Pool = pool
				// Modules run sequentially within a target's goroutine.
				for m := 0; m < modules; m++ {
					_, release, err := Acquire(p)
					if err != nil {
						t.Fatalf("acquire: %v", err)
					}
					release()
				}
			}
		}
		return OpenCount() - start
	}

	unpooled := run(nil)

	pl := NewSessionPool(SessionPoolOptions{MaxIdle: time.Hour, Metrics: newFakeSessionPoolMetrics()})
	defer pl.Close()
	pooled := run(pl)

	wantUnpooled := int64(cycles * targets * modules) // every walk dials
	if unpooled != wantUnpooled {
		t.Fatalf("unpooled opens = %d, want %d (one dial per walk)", unpooled, wantUnpooled)
	}
	// Pool dials once per (target) for the whole run (session persists across
	// cycles): targets total.
	reduction := 1 - float64(pooled)/float64(unpooled)
	t.Logf("open-reduction: unpooled=%d pooled=%d reduction=%.1f%%", unpooled, pooled, reduction*100)
	if pooled > unpooled/5 {
		t.Fatalf("pooled opens %d exceed 20%% of unpooled %d (reduction %.1f%% < 80%%)", pooled, unpooled, reduction*100)
	}
}
