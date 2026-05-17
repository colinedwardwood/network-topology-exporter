// Package limits holds the per-field byte caps shared by the federation push
// validator (internal/federation) and the snapshot loader (internal/snapshot).
//
// These are the canonical byte caps shared by the federation push validator
// and the snapshot loader. Raising any of these affects both wire-format
// acceptance and on-disk validation simultaneously: bumping a value in one
// place without the other will produce a configuration where the hub accepts
// a push that the snapshot loader will reject on the next process restart
// (or vice-versa). Keep the two paths locked together by importing from
// here, never by copying the constant into a new package.
package limits

// MaxDeviceIDBytes caps the byte length of a spoke-supplied device_id. The
// same cap applies to the on-disk snapshot loader so a snapshot written by an
// older release with a relaxed cap is rejected at load time rather than
// silently re-emitted.
const MaxDeviceIDBytes = 256

// MaxPortNameBytes caps the byte length of spoke-supplied port names and
// other edge / OOS string fields (src_device, src_port, dst_device, dst_port,
// discovery_proto, link_kind, reporting_device, reporting_port,
// neighbour_hint, proto). Same value as MaxDeviceIDBytes today, but kept
// distinct so the two can diverge if a future discovery protocol pushes the
// port-name shape beyond 256 bytes.
const MaxPortNameBytes = 256

// MaxLabelKeyBytes and MaxLabelValueBytes cap individual spoke-supplied
// label inputs before per-rune validation iterates the string. The
// http.MaxBytesReader on the push body bounds total payload size at 16 MiB,
// but a single 16 MiB label value would still force ~4M rune iterations in
// validateLabelValue — a CPU-DoS vector even against an mTLS-authenticated
// spoke. Prometheus / OpenMetrics impose no formal max on label values
// (REMEDIATION.md §3), but client_golang defaults and Grafana Cloud Mimir
// limits operate well under 4 KiB per value, so values exceeding 4096
// bytes are far outside any legitimate topology label and safe to reject.
const (
	MaxLabelKeyBytes   = 256
	MaxLabelValueBytes = 4096
)
