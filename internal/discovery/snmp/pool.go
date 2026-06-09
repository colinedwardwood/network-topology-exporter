package snmp

import (
	"sync"
	"time"

	g "github.com/gosnmp/gosnmp"
)

// SessionPoolMetrics is the observability sink for the SNMP session pool.
// As with WalkerMetrics, the interface lives in this package so the pool need
// not import the prometheus client library: the production implementation is
// an adapter wired in internal/metrics/ and injected via SessionPoolOptions.
// A nil sink is tolerated everywhere (the increment/set is dropped).
//
//   - RecordHit:        Checkout reused a live pooled session.
//   - RecordMiss:       Checkout opened a fresh session (cold key, or the
//     defensive in-use fallback that returns a transient non-pooled session).
//   - SetSize:          the current number of pooled sessions (gauge).
//   - RecordEviction:   a session was closed and removed, with reason ∈
//     {idle, credential_rotation, connection_error}.
type SessionPoolMetrics interface {
	RecordHit()
	RecordMiss()
	SetSize(int)
	RecordEviction(reason string)
}

// Eviction reasons for SessionPoolMetrics.RecordEviction. Closed low-cardinality set.
const (
	evictionReasonIdle = "idle"
	// evictionReasonCredentialRotation is a metric label value, not a secret;
	// gosec G101 trips on the "credential" substring in the identifier.
	evictionReasonCredentialRotation = "credential_rotation" //nolint:gosec // G101 false positive: metric label value, not a credential
	evictionReasonConnectionError    = "connection_error"
)

// poolKey identifies a pooled session. It MUST include the credential profile
// so a credential change for a target produces a distinct entry (the old
// session is left to idle-evict, or is explicitly removed by InvalidateProfile
// on rotation) and so rotation can target every session for a profile.
type poolKey struct {
	ip      string
	profile string
}

// poolEntry holds a pooled session plus its bookkeeping. A given entry is
// checked out to at most one caller at a time (inUse guards that), which is
// what makes reusing a non-goroutine-safe *gosnmp.GoSNMP across a target's
// sequential module walks safe.
type poolEntry struct {
	session  *g.GoSNMP
	lastUsed time.Time
	inUse    bool
}

// SessionPoolOptions configures a SessionPool.
type SessionPoolOptions struct {
	// MaxIdle is how long an unused session may sit in the pool before the
	// background evictor closes it. Must be > 0; the caller (config layer)
	// computes the default of 5 × discovery.interval.
	MaxIdle time.Duration

	// Metrics is the observability sink; nil drops all signals.
	Metrics SessionPoolMetrics
}

// SessionPool is an opt-in, concurrency-safe pool of SNMP sessions keyed by
// (target IP, credential profile). It exists to cut socket / conntrack churn
// on large fleets, where today every (target × enabled module) opens and closes
// a fresh UDP session per cycle (~9 sessions/target/cycle). With the pool, a
// target reuses ONE session across its modules within a cycle and across
// cycles, bounded at one session per (target × profile) (~50KB/session) — not
// per (target × module).
//
// Concurrency model: different targets run on different discovery worker
// goroutines, so the pool's map access is mutex-protected. A single key is only
// ever checked out once at a time (guaranteed because a target's modules run
// sequentially in its per-device goroutine; see internal/app/cycle.go). The
// defensive in-use guard in acquire enforces this even if the invariant were
// ever violated: a second concurrent checkout of an in-use key falls back to a
// transient, non-pooled session (counted as a miss) so a live *gosnmp.GoSNMP is
// never aliased across goroutines.
//
// Credential retention tradeoff (issue #5 / LD-12): a pooled session holds its
// own copy of the credential inside gosnmp (Community for v2c, the
// UsmSecurityParameters passphrases for v3). The per-cycle params.Zeroize in
// cycle.go wipes the Params byte slices but CANNOT reach gosnmp's internal
// copy — that is expected, because the session needs working credentials to be
// reused. The consequence is that pooled sessions retain plaintext credentials
// in process memory until they are evicted. To bound that exposure, every path
// that removes a session (idle eviction, InvalidateProfile on rotation, and
// Close on shutdown) CLOSES the gosnmp session and best-effort clears its
// credential material via clearSessionCredentials. Operators rotating
// credentials should call InvalidateProfile(name) so rotated credentials do not
// linger; see docs/operator/scale.md and docs/operator/security.md.
type SessionPool struct {
	mu       sync.Mutex
	sessions map[poolKey]*poolEntry
	maxIdle  time.Duration
	metrics  SessionPoolMetrics

	stop   chan struct{}
	stopWG sync.WaitGroup
	closed bool
}

// NewSessionPool constructs a pool and starts its background idle-eviction
// goroutine. Callers MUST call Close to stop the goroutine and release all
// sessions. A non-positive MaxIdle is treated as "no idle eviction" (the
// evictor still runs but never evicts) — the config layer always supplies a
// positive default, so this only guards programmatic misuse.
func NewSessionPool(opts SessionPoolOptions) *SessionPool {
	pl := &SessionPool{
		sessions: make(map[poolKey]*poolEntry),
		maxIdle:  opts.MaxIdle,
		metrics:  opts.Metrics,
		stop:     make(chan struct{}),
	}
	// Sweep at a fraction of maxIdle so a stale session is closed within roughly
	// one maxIdle window of going idle. Floor the tick so a tiny test maxIdle
	// still produces a sane (non-zero, non-busy) interval.
	tick := pl.maxIdle / 2
	if tick <= 0 {
		tick = time.Minute
	}
	pl.stopWG.Add(1)
	go pl.evictLoop(tick)
	return pl
}

// acquire is the pool-backed branch of Acquire. It returns a session and a
// release func that returns the session to the pool (or closes a transient
// fallback session). The error path returns a no-op release.
func (pl *SessionPool) acquire(p Params) (*g.GoSNMP, func(), error) {
	key := poolKey{ip: p.IP.String(), profile: p.CredentialProfile}

	pl.mu.Lock()
	if pl.closed {
		// Pool is shutting down: behave like the fresh path so in-flight walks
		// still complete, but do not store anything.
		pl.mu.Unlock()
		return pl.openTransient(p)
	}
	e, ok := pl.sessions[key]
	if ok && !e.inUse {
		e.inUse = true
		e.lastUsed = time.Now()
		s := e.session
		pl.recordHitLocked()
		pl.mu.Unlock()
		return s, pl.releaseFunc(key), nil
	}
	if ok && e.inUse {
		// Defensive guard: the key is already checked out. This should never
		// happen given sequential modules per target, but never alias a live
		// *gosnmp across goroutines — hand out a transient non-pooled session
		// and count it as a miss.
		pl.mu.Unlock()
		return pl.openTransient(p)
	}
	pl.mu.Unlock()

	// Cold key: open a fresh session outside the lock (Connect does a UDP dial),
	// then store it. Count as a miss.
	client, err := Open(p)
	if err != nil {
		pl.recordMiss()
		return nil, func() {}, err
	}
	pl.mu.Lock()
	if pl.closed {
		// Raced with Close: don't store; hand out the fresh session with a
		// closing release so the connection is not leaked.
		pl.mu.Unlock()
		pl.recordMiss()
		return client, func() { _ = client.Conn.Close() }, nil
	}
	// Re-check: another goroutine for the same key may have stored an entry
	// while we dialed (only possible across targets sharing an IP+profile, which
	// the sequential-modules invariant does not cover). If so, keep ours as a
	// transient and leave theirs in place to avoid aliasing.
	if existing, ok := pl.sessions[key]; ok && !existing.inUse {
		existing.inUse = true
		existing.lastUsed = time.Now()
		s := existing.session
		pl.recordHitLocked()
		pl.mu.Unlock()
		_ = client.Conn.Close()
		return s, pl.releaseFunc(key), nil
	}
	if _, ok := pl.sessions[key]; ok {
		// Existing entry is in use; keep our fresh one transient.
		pl.mu.Unlock()
		pl.recordMiss()
		return client, func() { _ = client.Conn.Close() }, nil
	}
	pl.sessions[key] = &poolEntry{session: client, lastUsed: time.Now(), inUse: true}
	pl.setSizeLocked()
	pl.recordMiss()
	pl.mu.Unlock()
	return client, pl.releaseFunc(key), nil
}

// openTransient opens a fresh, non-pooled session whose release closes it.
// Used for the shutdown and defensive-in-use fallback paths. Counted as a miss.
func (pl *SessionPool) openTransient(p Params) (*g.GoSNMP, func(), error) {
	client, err := Open(p)
	if err != nil {
		pl.recordMiss()
		return nil, func() {}, err
	}
	pl.recordMiss()
	return client, func() { _ = client.Conn.Close() }, nil
}

// releaseFunc returns the release closure for a pooled key. The closure marks
// the entry not-in-use and refreshes lastUsed; the gosnmp session is NOT closed
// (it stays in the pool for reuse). It tolerates the entry having been removed
// in the meantime (e.g. by InvalidateProfile mid-walk).
func (pl *SessionPool) releaseFunc(key poolKey) func() {
	return func() { pl.returnKey(key) }
}

func (pl *SessionPool) returnKey(key poolKey) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	if e, ok := pl.sessions[key]; ok {
		e.inUse = false
		e.lastUsed = time.Now()
	}
}

// Return hands a session back to the pool. When healthy is false (the walk hit
// a connection-level error), the session is closed and evicted with reason
// connection_error so the next acquire dials fresh rather than reusing a dead
// socket. When healthy, the entry is marked available and its idle clock reset.
func (pl *SessionPool) Return(p Params, s *g.GoSNMP, healthy bool) {
	key := poolKey{ip: p.IP.String(), profile: p.CredentialProfile}
	pl.mu.Lock()
	defer pl.mu.Unlock()
	e, ok := pl.sessions[key]
	if !ok || e.session != s {
		// Not a pooled session (e.g. a transient fallback). If unhealthy, close it.
		if !healthy {
			closeSession(s)
		}
		return
	}
	if !healthy {
		closeSession(e.session)
		delete(pl.sessions, key)
		pl.setSizeLocked()
		pl.recordEviction(evictionReasonConnectionError)
		return
	}
	e.inUse = false
	e.lastUsed = time.Now()
}

// InvalidateProfile closes and removes every pooled session whose key carries
// the given credential profile name, recording a credential_rotation eviction
// for each. It is the rotation hook: when an operator rotates a profile's
// credentials, calling this guarantees the next walk dials with the new
// credentials and that the old plaintext credentials held inside the gosnmp
// sessions do not linger in memory.
//
// NOTE / TODO(#83): the credential resolver (internal/credentials) does not yet
// expose a rotation callback. Until it does, this method is exercised by tests
// and is safe to call from any future resolver rotation hook; wire that hook to
// call pool.InvalidateProfile(name) when a profile's secret changes.
func (pl *SessionPool) InvalidateProfile(name string) {
	pl.mu.Lock()
	defer pl.mu.Unlock()
	for k, e := range pl.sessions {
		if k.profile == name {
			closeSession(e.session)
			delete(pl.sessions, k)
			pl.recordEviction(evictionReasonCredentialRotation)
		}
	}
	pl.setSizeLocked()
}

// Evict closes and removes every not-in-use session whose lastUsed is older
// than maxIdle, recording an idle eviction for each. In-use sessions are never
// touched. Exported for tests; the background loop calls it on a timer.
func (pl *SessionPool) Evict() {
	if pl.maxIdle <= 0 {
		return
	}
	cutoff := time.Now().Add(-pl.maxIdle)
	pl.mu.Lock()
	defer pl.mu.Unlock()
	for k, e := range pl.sessions {
		if !e.inUse && e.lastUsed.Before(cutoff) {
			closeSession(e.session)
			delete(pl.sessions, k)
			pl.recordEviction(evictionReasonIdle)
		}
	}
	pl.setSizeLocked()
}

func (pl *SessionPool) evictLoop(tick time.Duration) {
	defer pl.stopWG.Done()
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-pl.stop:
			return
		case <-t.C:
			pl.Evict()
		}
	}
}

// Close shuts the pool down: it stops the background evictor and closes every
// pooled session (clearing credential material). It is idempotent. After Close,
// acquire falls back to transient fresh sessions so any in-flight walk still
// completes, but nothing is pooled.
func (pl *SessionPool) Close() {
	pl.mu.Lock()
	if pl.closed {
		pl.mu.Unlock()
		return
	}
	pl.closed = true
	close(pl.stop)
	for k, e := range pl.sessions {
		closeSession(e.session)
		delete(pl.sessions, k)
	}
	pl.setSizeLocked()
	pl.mu.Unlock()
	pl.stopWG.Wait()
}

// --- metrics helpers (nil-tolerant) ---

func (pl *SessionPool) recordHitLocked() {
	if pl.metrics != nil {
		pl.metrics.RecordHit()
	}
}

func (pl *SessionPool) recordMiss() {
	if pl.metrics != nil {
		pl.metrics.RecordMiss()
	}
}

func (pl *SessionPool) recordEviction(reason string) {
	if pl.metrics != nil {
		pl.metrics.RecordEviction(reason)
	}
}

// setSizeLocked publishes the current pool size. Caller holds pl.mu.
func (pl *SessionPool) setSizeLocked() {
	if pl.metrics != nil {
		pl.metrics.SetSize(len(pl.sessions))
	}
}

// closeSession closes a gosnmp session's connection and best-effort clears the
// plaintext credential material it holds, bounding the credential-retention
// window described on SessionPool.
func closeSession(s *g.GoSNMP) {
	if s == nil {
		return
	}
	if s.Conn != nil {
		_ = s.Conn.Close()
	}
	clearSessionCredentials(s)
}

// clearSessionCredentials zeroes the credential fields gosnmp keeps on the
// session struct. These are Go strings (immutable) so we cannot wipe the
// backing array, but dropping the references makes the secrets eligible for GC
// rather than pinned for the session's former lifetime. Best-effort by design;
// see the credential-retention note on SessionPool and docs/operator/security.md.
func clearSessionCredentials(s *g.GoSNMP) {
	s.Community = ""
	if usm, ok := s.SecurityParameters.(*g.UsmSecurityParameters); ok && usm != nil {
		usm.AuthenticationPassphrase = ""
		usm.PrivacyPassphrase = ""
	}
}
