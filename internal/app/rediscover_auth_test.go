package app

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWebConfigHasClientAuth guards the /admin/rediscover auth predicate.
// The critical case is TLS-only: a web-config that sets server TLS but no
// client authentication encrypts the channel without authenticating the
// caller, so it must NOT enable the privileged endpoint (the handler must
// keep returning 403). Failing this test reopens a remote, unauthenticated
// self-DoS vector.
func TestWebConfigHasClientAuth(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want bool
	}{
		{"empty path", "", false}, // handled via path=="" below
		{"comment only", "# nothing\n", false},
		{"tls server only (encrypt, no client auth)", "tls_server_config:\n  cert_file: a.pem\n  key_file: a.key\n", false},
		{"tls request-only client cert (optional)", "tls_server_config:\n  cert_file: a.pem\n  key_file: a.key\n  client_auth_type: RequestClientCert\n", false},
		{"tls verify-if-given (optional)", "tls_server_config:\n  client_auth_type: VerifyClientCertIfGiven\n", false},
		{"basic auth users", "basic_auth_users:\n  admin: $2y$10$hash\n", true},
		{"mTLS require-any", "tls_server_config:\n  client_auth_type: RequireAnyClientCert\n", true},
		{"mTLS require-and-verify", "tls_server_config:\n  client_auth_type: RequireAndVerifyClientCert\n", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "empty path" {
				if WebConfigHasClientAuth("") {
					t.Fatal("empty path: want false")
				}
				return
			}
			p := filepath.Join(t.TempDir(), "web-config.yml")
			if err := os.WriteFile(p, []byte(tc.yaml), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := WebConfigHasClientAuth(p); got != tc.want {
				t.Errorf("WebConfigHasClientAuth(%q) = %v, want %v", tc.yaml, got, tc.want)
			}
		})
	}

	// A path that does not exist must fail closed (false), not panic.
	if WebConfigHasClientAuth(filepath.Join(t.TempDir(), "absent.yml")) {
		t.Error("nonexistent web-config: want false (fail closed)")
	}
}
