package discovery

import (
	"errors"
	"testing"
)

// TestPolicyErrorError covers the Error() method with and without a wrapped error.
func TestPolicyErrorError(t *testing.T) {
	t.Run("with_wrapped_error", func(t *testing.T) {
		inner := errors.New("inner cause")
		pe := &PolicyError{Module: "lldp", Reason: "missing_table", Err: inner}
		got := pe.Error()
		if got != "lldp policy failure: missing_table: inner cause" {
			t.Errorf("Error() = %q, want %q", got, "lldp policy failure: missing_table: inner cause")
		}
	})

	t.Run("without_wrapped_error", func(t *testing.T) {
		pe := &PolicyError{Module: "fdb", Reason: "required_table_partial_decode", Err: nil}
		got := pe.Error()
		if got != "fdb policy failure: required_table_partial_decode" {
			t.Errorf("Error() = %q, want %q", got, "fdb policy failure: required_table_partial_decode")
		}
	})
}

// TestPolicyErrorUnwrap covers Unwrap() returning the wrapped error or nil.
func TestPolicyErrorUnwrap(t *testing.T) {
	t.Run("non_nil_err", func(t *testing.T) {
		inner := errors.New("root cause")
		pe := &PolicyError{Module: "bgp", Reason: "some_reason", Err: inner}
		if got := pe.Unwrap(); got != inner {
			t.Errorf("Unwrap() = %v, want %v", got, inner)
		}
	})

	t.Run("nil_err", func(t *testing.T) {
		pe := &PolicyError{Module: "ospf", Reason: "some_reason", Err: nil}
		if got := pe.Unwrap(); got != nil {
			t.Errorf("Unwrap() = %v, want nil", got)
		}
	})
}

// TestJoinReasonCodes covers all branches of JoinReasonCodes.
func TestJoinReasonCodes(t *testing.T) {
	cases := []struct {
		name  string
		input []string
		want  string
	}{
		{
			name:  "empty_input",
			input: []string{},
			want:  "",
		},
		{
			name:  "single_reason",
			input: []string{"missing_srcport_mapping"},
			want:  "missing_srcport_mapping",
		},
		{
			name:  "duplicates_deduplicated",
			input: []string{"a", "b", "a"},
			want:  "a,b",
		},
		{
			name:  "empty_strings_skipped",
			input: []string{"a", "", "b"},
			want:  "a,b",
		},
		{
			name:  "all_blanks",
			input: []string{"", ""},
			want:  "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := JoinReasonCodes(tc.input)
			if got != tc.want {
				t.Errorf("JoinReasonCodes(%v) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
