// Credential-zeroization helpers for snmp.Params.
//
// Motivation: SNMPv2c community strings and SNMPv3 auth/priv keys live in
// process memory for the duration of every discovery cycle. A core dump,
// container memory snapshot, or /proc/<pid>/mem read leaks every credential
// the process has handled. Storing credentials in []byte (instead of the
// immutable Go string type) lets the caller overwrite the backing storage
// with zeros once the credentials are no longer needed.
//
// Best-effort only: the Go runtime may copy bytes during garbage-collection
// or stack-grow events, and any conversion to string (e.g., when passing
// credentials to the upstream gosnmp library) creates an immutable copy
// that this code cannot reach. See docs/operator/security.md for the full
// threat model.

package snmp

// Zeroize overwrites the credential byte slices inside p with zeros and
// drops the references so the underlying storage becomes garbage-collectable.
// Safe to call on a zero-value Params or one that has already been zeroized.
//
// Best-effort only:
//   - The Go GC may have copied these bytes elsewhere (stack growth, escape
//     analysis, slice resizing). Zeroize cannot reach those copies.
//   - Any conversion to string at the gosnmp boundary (see buildClient) makes
//     an immutable copy that Zeroize cannot reach. The gosnmp library retains
//     that copy for the lifetime of the *gosnmp.GoSNMP session.
//
// See docs/operator/security.md.
func (p *Params) Zeroize() {
	if p == nil {
		return
	}
	zeroBytes(p.Community)
	zeroBytes(p.AuthKey)
	zeroBytes(p.PrivKey)
	p.Community = nil
	p.AuthKey = nil
	p.PrivKey = nil
}

// zeroBytes overwrites every byte of b with zero. A simple loop is used
// instead of crypto/subtle.ConstantTimeCopy: this is data destruction, not
// constant-time comparison, and timing side-channels on the zeroing operation
// are not in the threat model.
func zeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
