// Package credentials implements LD-12: named SNMP profiles, per-device
// resolution, a token-bucket trial limiter, and a per-device winning-profile
// cache.
//
// Resolution order for a target IP:
//  1. exact per-IP assignment    → the configured profile list
//  2. most-specific CIDR match   → the configured profile list
//  3. fallback_order             → profiles in config order
//
// The cache is keyed on device IP so CachedProfile lookups at walk time are
// consistent with RecordSuccess/RecordFailure; it is loaded from the LD-13
// snapshot on startup and written back after every cycle.
package credentials

import (
	"context"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/grafana/network-topology-exporter/internal/config"
)

// cacheTTL is the maximum age of a cached credential entry. Entries older than
// this are treated as stale and evicted on the next CachedProfile lookup. Two
// hours gives enough headroom for a missed cycle while ensuring that a
// decommissioned or re-addressed device does not retain a wrong profile
// indefinitely.
const cacheTTL = 2 * time.Hour

// cacheEntry pairs a resolved profile name with the time it was cached.
type cacheEntry struct {
	profile  string
	cachedAt time.Time
}

// Resolver implements the LD-12 credential-resolution contract against the
// validated config. It is safe for concurrent use; the rate-limiter and the
// cache are both internally synchronised.
type Resolver struct {
	profiles map[string]config.CredentialProfile // by name
	exact    map[string][]string                 // ip → profile names
	cidrs    []cidrAssignment
	fallback []string
	limiter  *tokenBucket

	mu    sync.RWMutex
	cache map[string]cacheEntry // IP string → profile entry
}

type cidrAssignment struct {
	net       *net.IPNet
	prefixLen int
	profiles  []string
}

// New builds a Resolver from validated CredentialsConfig. Returns an error
// only if a CIDR fails to parse, which the config validator should have
// caught first; treat a non-nil error as a programmer mistake.
func New(c config.CredentialsConfig) (*Resolver, error) {
	r := &Resolver{
		profiles: make(map[string]config.CredentialProfile, len(c.Profiles)),
		exact:    make(map[string][]string),
		fallback: cloneStrings(c.FallbackOrder),
		limiter:  newTokenBucket(c.TrialRatePerSecond),
		cache:    make(map[string]cacheEntry),
	}
	for _, p := range c.Profiles {
		r.profiles[p.Name] = p
	}
	for _, a := range c.Assignments {
		switch {
		case a.IP != "":
			r.exact[a.IP] = cloneStrings(a.Profiles)
		case a.CIDR != "":
			_, n, err := net.ParseCIDR(a.CIDR)
			if err != nil {
				return nil, err
			}
			ones, _ := n.Mask.Size()
			r.cidrs = append(r.cidrs, cidrAssignment{
				net:       n,
				prefixLen: ones,
				profiles:  cloneStrings(a.Profiles),
			})
		}
	}
	// Most-specific CIDR wins — sort longest prefix first so Resolve picks
	// the right bucket on the first hit.
	sort.SliceStable(r.cidrs, func(i, j int) bool {
		return r.cidrs[i].prefixLen > r.cidrs[j].prefixLen
	})
	return r, nil
}

// Resolve returns the profile list to try for ip in priority order.
func (r *Resolver) Resolve(ip net.IP) []string {
	if ps, ok := r.exact[ip.String()]; ok {
		return cloneStrings(ps)
	}
	for _, a := range r.cidrs {
		if a.net.Contains(ip) {
			return cloneStrings(a.profiles)
		}
	}
	return cloneStrings(r.fallback)
}

// RecordSuccess updates the per-device cache so the next cycle bypasses the
// trial sequence for this device. Caller is the discovery worker after a
// successful walk.
func (r *Resolver) RecordSuccess(deviceID, profileName string) {
	r.mu.Lock()
	r.cache[deviceID] = cacheEntry{profile: profileName, cachedAt: time.Now()}
	r.mu.Unlock()
}

// RecordFailure invalidates the cached profile for deviceID. The next cycle
// re-enters the trial sequence.
func (r *Resolver) RecordFailure(deviceID string) {
	r.mu.Lock()
	delete(r.cache, deviceID)
	r.mu.Unlock()
}

// CachedProfile returns the cached winning profile for deviceID, if any.
// Discovery workers should consult this before Resolve so already-known
// devices skip the trial path. Entries older than cacheTTL are evicted and
// treated as a cache miss so stale credentials for decommissioned or
// re-addressed devices are not reused indefinitely.
func (r *Resolver) CachedProfile(deviceID string) (string, bool) {
	r.mu.RLock()
	entry, ok := r.cache[deviceID]
	r.mu.RUnlock()
	if !ok {
		return "", false
	}
	if time.Since(entry.cachedAt) > cacheTTL {
		r.mu.Lock()
		// Re-check under write lock: another goroutine may have refreshed or
		// already deleted the entry while we were waiting to upgrade.
		if e, still := r.cache[deviceID]; still && time.Since(e.cachedAt) > cacheTTL {
			delete(r.cache, deviceID)
		}
		r.mu.Unlock()
		return "", false
	}
	return entry.profile, true
}

// LoadCache replaces the in-memory cache with the contents of the LD-13
// snapshot. Called once at startup. Loaded entries are stamped with the
// current time so they are treated as freshly valid; this avoids a snapshot
// format change while still allowing the TTL to expire them normally.
func (r *Resolver) LoadCache(cache map[string]string) {
	now := time.Now()
	r.mu.Lock()
	r.cache = make(map[string]cacheEntry, len(cache))
	for k, v := range cache {
		r.cache[k] = cacheEntry{profile: v, cachedAt: now}
	}
	r.mu.Unlock()
}

// SnapshotCache returns a copy of the current cache for snapshot writing.
// Only live (non-expired) entries are included; timestamps are stripped so the
// on-disk format stays as map[string]string.
func (r *Resolver) SnapshotCache() map[string]string {
	r.mu.RLock()
	out := make(map[string]string, len(r.cache))
	for k, e := range r.cache {
		if time.Since(e.cachedAt) <= cacheTTL {
			out[k] = e.profile
		}
	}
	r.mu.RUnlock()
	return out
}

// Profile returns the resolved profile by name. Useful for the SNMP module
// when it needs the actual credential type to build a session.
func (r *Resolver) Profile(name string) (config.CredentialProfile, bool) {
	p, ok := r.profiles[name]
	return p, ok
}

// AcquireTrial blocks until the token bucket allows another credential trial
// or ctx is cancelled. Every credential trial — cache hit or fallback — must
// pass through here so the global trial rate stays bounded.
func (r *Resolver) AcquireTrial(ctx context.Context) error {
	return r.limiter.acquire(ctx)
}

// tokenBucket is a minimal token bucket. Refills at ratePerSecond tokens per
// second up to a burst of ratePerSecond. Adequate for the LD-12 trial limiter;
// we don't need a full leaky-bucket library for one-token-per-second granularity.
type tokenBucket struct {
	mu         sync.Mutex
	rate       int
	tokens     int
	lastRefill time.Time
}

func newTokenBucket(rate int) *tokenBucket {
	if rate < 1 {
		rate = 1
	}
	if rate > 1_000_000 {
		rate = 1_000_000 // interval = 1µs, well above OS timer resolution
	}
	return &tokenBucket{rate: rate, tokens: rate, lastRefill: time.Now()}
}

func (b *tokenBucket) acquire(ctx context.Context) error {
	interval := time.Second / time.Duration(b.rate)
	for {
		b.mu.Lock()
		b.refillLocked(time.Now())
		if b.tokens > 0 {
			b.tokens--
			b.mu.Unlock()
			return nil
		}
		b.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		t := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}

func (b *tokenBucket) refillLocked(now time.Time) {
	elapsed := now.Sub(b.lastRefill)
	if elapsed <= 0 {
		return
	}
	add := int(elapsed.Seconds() * float64(b.rate))
	if add <= 0 {
		return
	}
	b.tokens += add
	if b.tokens > b.rate {
		b.tokens = b.rate
	}
	// Advance lastRefill by exactly the time consumed to preserve the
	// fractional-token remainder for the next refill call.
	b.lastRefill = b.lastRefill.Add(time.Duration(add) * time.Second / time.Duration(b.rate))
}

func cloneStrings(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	return out
}
