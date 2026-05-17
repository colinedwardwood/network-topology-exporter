package snmp

import (
	"net"
	"testing"
)

// TestParamsZeroize verifies that Zeroize overwrites the underlying credential
// bytes (so a snapshot of the slice header captured before the call now points
// at all-zero memory) and that the Params fields themselves are reset to nil.
// Both checks matter: the byte-overwrite is the security-relevant
// behaviour (issue #5); the nil-out prevents accidental re-use.
func TestParamsZeroize(t *testing.T) {
	community := []byte("public-secret-1234")
	authKey := []byte("auth-passphrase-abc")
	privKey := []byte("priv-passphrase-xyz")

	// Capture the underlying byte-slice references so we can assert on them
	// after Zeroize has overwritten the Params fields.
	communityRef := community
	authKeyRef := authKey
	privKeyRef := privKey

	p := &Params{
		IP:        net.ParseIP("192.0.2.1"),
		Community: community,
		V3:        true,
		Username:  "admin",
		AuthKey:   authKey,
		PrivKey:   privKey,
	}

	p.Zeroize()

	if p.Community != nil {
		t.Errorf("Community = %v, want nil", p.Community)
	}
	if p.AuthKey != nil {
		t.Errorf("AuthKey = %v, want nil", p.AuthKey)
	}
	if p.PrivKey != nil {
		t.Errorf("PrivKey = %v, want nil", p.PrivKey)
	}

	// Non-credential fields must be untouched (Zeroize is for credentials only).
	if p.Username != "admin" {
		t.Errorf("Username = %q, want admin (Zeroize must not touch non-credential fields)", p.Username)
	}
	if !p.V3 {
		t.Error("V3 was cleared by Zeroize; only credential byte slices should be touched")
	}

	// The byte slices that previously backed the credentials must now be all
	// zeros — this is the actual security-relevant invariant.
	for i, b := range communityRef {
		if b != 0 {
			t.Errorf("Community byte %d = %#x after Zeroize, want 0", i, b)
		}
	}
	for i, b := range authKeyRef {
		if b != 0 {
			t.Errorf("AuthKey byte %d = %#x after Zeroize, want 0", i, b)
		}
	}
	for i, b := range privKeyRef {
		if b != 0 {
			t.Errorf("PrivKey byte %d = %#x after Zeroize, want 0", i, b)
		}
	}
}

// TestParamsZeroizeNilSafe verifies Zeroize is safe to call on a zero-value
// Params (nil byte slices) and on an already-zeroized Params. Both cases must
// not panic so callers can use Zeroize from deferreds without pre-checks.
func TestParamsZeroizeNilSafe(t *testing.T) {
	p := &Params{}
	p.Zeroize() // empty params
	p.Zeroize() // double-call

	// Also verify the nil-receiver guard inside Zeroize.
	var np *Params
	np.Zeroize()
}
