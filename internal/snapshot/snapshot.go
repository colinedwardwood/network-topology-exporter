// Package snapshot implements versioned JSON persistence of the reconciled
// graph plus the credential cache. The exporter loads the snapshot at startup
// so /metrics serves the previous-cycle graph immediately
// (with network_topology_graph_stale=1) instead of going dark while the
// first live cycle runs.
//
// On-disk layout is a single JSON document. Writes go via tmp → fsync →
// rename, so a crash mid-write leaves either the previous good snapshot or
// the new one — never a half-written file.
package snapshot

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
)

// tmpFile is the subset of *os.File used by Write. Defined as an interface so
// tests can inject failures for individual file operations.
type tmpFile interface {
	Name() string
	Write(b []byte) (int, error)
	Sync() error
	Close() error
}

// quarantineFile is the subset of *os.File used by quarantine.
type quarantineFile interface {
	Write(b []byte) (int, error)
	Close() error
}

// Package-level vars wrapping OS calls. Tests override these to inject errors;
// production code always uses the real implementations.
var (
	readFileFn   = os.ReadFile
	marshalFn    = json.Marshal
	mkdirAllFn   = os.MkdirAll
	createTempFn = func(dir, pattern string) (tmpFile, error) { return os.CreateTemp(dir, pattern) }
	renameFn     = os.Rename
	openFileFn   = func(name string, flag int, perm fs.FileMode) (quarantineFile, error) {
		return os.OpenFile(name, flag, perm) //nolint:gosec
	}
)

// CurrentVersion is the on-disk schema version. Bump when the persisted
// shape changes; older snapshots are discarded with a warning rather than
// silently mis-parsed.
//
// Version history:
//
//	1 — initial format
//	2 — EdgeKeyString escapes "|" as "%7C" in UnconfirmedAges keys
//	3 — EdgeKeyString escapes "%" as "%25" in addition to "|" as "%7C";
//	    "%" must be escaped first to prevent double-encoding.
const CurrentVersion = 3

// Per-field byte caps applied by validateSnapshotFields. These guard against a
// malicious or corrupted snapshot.json declaring multi-megabyte strings that
// json.Unmarshal would happily allocate. Values mirror the caps used elsewhere
// in the codebase (federation hub) and are duplicated here as local constants
// to keep this package self-contained; a later cleanup can consolidate.
const (
	maxIDBytes         = 256  // device IDs, sysNames, port names
	maxShortFieldBytes = 256  // vendor, model, OS version, site, src/dst device
	maxPortBytes       = 256  // port names
	maxProtoBytes      = 64   // enum-like values: discovery_proto, link_kind
	maxLabelKeyBytes   = 256  // map key for Labels / Metadata
	maxLabelValueBytes = 4096 // map value for Labels / Metadata
)

// File is the on-disk representation. Public fields and field tags are part
// of the persistence contract; treat schema changes the same as a database
// migration.
type File struct {
	Version         int                             `json:"version"`
	WrittenAt       time.Time                       `json:"written_at"`
	Devices         []discovery.Device              `json:"devices"`
	Edges           []discovery.Edge                `json:"edges"`
	OutOfScope      []discovery.OutOfScopeNeighbour `json:"out_of_scope"`
	CredentialCache map[string]string               `json:"credential_cache"` // IP string → profile name (LD-12)
	UnconfirmedAges map[string]int                  `json:"unconfirmed_ages"` // edge-id → consecutive unconfirmed cycles (LD-14)
}

// ErrVersionMismatch is reported when the on-disk version is unrecognised.
// Callers treat this as cold-start: log, discard the file, continue with
// an empty graph. Falling back is the documented behaviour, not an error
// the operator should escalate.
var ErrVersionMismatch = errors.New("snapshot: unrecognised version")

// Load reads the snapshot at path. Returns (nil, nil) when the file does
// not exist — first run is not an error. Returns ErrVersionMismatch wrapped
// when the file exists but its version is unknown.
func Load(path string) (*File, error) {
	b, err := readFileFn(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read snapshot %q: %w", path, err)
	}
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		if qErr := quarantine(path, b); qErr != nil {
			return nil, fmt.Errorf("parse snapshot %q: %w (quarantine also failed: %v)", path, err, qErr)
		}
		return nil, fmt.Errorf("parse snapshot %q: %w", path, err)
	}
	if f.Version != CurrentVersion {
		if qErr := quarantine(path, b); qErr != nil {
			return nil, fmt.Errorf("%w: got %d, want %d (quarantine also failed: %v)", ErrVersionMismatch, f.Version, CurrentVersion, qErr)
		}
		return nil, fmt.Errorf("%w: got %d, want %d", ErrVersionMismatch, f.Version, CurrentVersion)
	}
	if err := validateSnapshotFields(&f); err != nil {
		return nil, fmt.Errorf("validate snapshot: %w", err)
	}
	return &f, nil
}

func quarantine(path string, contents []byte) error {
	const maxAttempts = 100
	for i := 0; i < maxAttempts; i++ {
		dst := path + ".bad"
		if i > 0 {
			dst = fmt.Sprintf("%s.bad.%d", path, i)
		}
		f, err := openFileFn(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return err
		}
		if _, err := f.Write(contents); err != nil {
			_ = f.Close()
			_ = os.Remove(dst)
			return err
		}
		if err := f.Close(); err != nil {
			_ = os.Remove(dst)
			return err
		}
		return os.Remove(path)
	}
	// All slots taken — remove original to prevent repeat-startup hang
	return os.Remove(path)
}

// Write persists f to path atomically. The temp file lives next to the
// final path so rename is on the same filesystem; rename is atomic on POSIX
// and replace-on-rename on Windows. fsync runs on the data file before the
// rename so a power loss between write and rename can't promote a partial
// write to the final path.
func Write(path string, f File) error {
	if f.Version == 0 {
		f.Version = CurrentVersion
	}
	if f.WrittenAt.IsZero() {
		f.WrittenAt = time.Now().UTC()
	}
	b, err := marshalFn(f)
	if err != nil {
		return fmt.Errorf("marshal snapshot: %w", err)
	}
	dir := filepath.Dir(path)
	if err := mkdirAllFn(dir, 0o700); err != nil {
		return fmt.Errorf("ensure snapshot dir %q: %w", dir, err)
	}
	tmp, err := createTempFn(dir, ".snapshot-*.json.tmp")
	if err != nil {
		return fmt.Errorf("create temp snapshot: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp snapshot: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("fsync temp snapshot: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp snapshot: %w", err)
	}
	if err := renameFn(tmpName, path); err != nil {
		cleanup()
		return fmt.Errorf("rename temp snapshot to %q: %w", path, err)
	}
	// Fsync the parent directory so the renamed directory entry is durable.
	// Best-effort: some filesystems (NFS, FAT) don't support directory fsync.
	if fd, err := os.Open(dir); err == nil { //nolint:gosec
		_ = fd.Sync()
		_ = fd.Close()
	}
	return nil
}

// maxValidationErrors caps the number of per-field errors validateSnapshotFields
// will accumulate before stopping. Without this bound a deliberately-corrupt
// snapshot with thousands of oversized fields could produce a multi-MB error
// message. Operators recovering from corruption see the first 100 problems
// plus an "omitted" sentinel and can fix them in batches.
const maxValidationErrors = 100

// validateSnapshotFields enforces per-field length caps on the parsed snapshot.
// It guards against a corrupted or hostile snapshot.json declaring multi-MB
// strings that json.Unmarshal would happily allocate. Error messages include
// the slice index and the actual byte length so an operator can locate the
// offending entry.
//
// Errors are accumulated (not returned on first failure) and joined via
// errors.Join so an operator recovering from a corrupted snapshot can see
// every offending field in one pass instead of fix-reload-repeat. The
// accumulation is capped at maxValidationErrors to prevent a hostile file
// from producing an unbounded error message.
func validateSnapshotFields(f *File) error {
	if f == nil {
		return nil
	}
	var errs []error
	// addErr appends a formatted error and returns true while there is still
	// room under the cap. Callers may either ignore the return value (the next
	// addErr call is itself a no-op once capped) or short-circuit on false.
	addErr := func(format string, args ...any) bool {
		if len(errs) >= maxValidationErrors {
			return false
		}
		errs = append(errs, fmt.Errorf(format, args...))
		return true
	}

	for i, d := range f.Devices {
		if len(errs) >= maxValidationErrors {
			break
		}
		if n := len(d.ID); n > maxIDBytes {
			addErr("device[%d]: id exceeds %d bytes (%d)", i, maxIDBytes, n)
		}
		if n := len(d.Vendor); n > maxShortFieldBytes {
			addErr("device[%d]: vendor exceeds %d bytes (%d)", i, maxShortFieldBytes, n)
		}
		if n := len(d.Model); n > maxShortFieldBytes {
			addErr("device[%d]: model exceeds %d bytes (%d)", i, maxShortFieldBytes, n)
		}
		if n := len(d.OSVersion); n > maxShortFieldBytes {
			addErr("device[%d]: os_version exceeds %d bytes (%d)", i, maxShortFieldBytes, n)
		}
		if n := len(d.Site); n > maxShortFieldBytes {
			addErr("device[%d]: site exceeds %d bytes (%d)", i, maxShortFieldBytes, n)
		}
		for k, v := range d.Labels {
			if len(errs) >= maxValidationErrors {
				break
			}
			if n := len(k); n > maxLabelKeyBytes {
				addErr("device[%d]: labels key exceeds %d bytes (%d)", i, maxLabelKeyBytes, n)
			}
			if n := len(v); n > maxLabelValueBytes {
				addErr("device[%d]: labels value for key %q exceeds %d bytes (%d)", i, k, maxLabelValueBytes, n)
			}
		}
	}
	for i, e := range f.Edges {
		if len(errs) >= maxValidationErrors {
			break
		}
		if n := len(e.SrcDevice); n > maxIDBytes {
			addErr("edge[%d]: src_device exceeds %d bytes (%d)", i, maxIDBytes, n)
		}
		if n := len(e.DstDevice); n > maxIDBytes {
			addErr("edge[%d]: dst_device exceeds %d bytes (%d)", i, maxIDBytes, n)
		}
		if n := len(e.SrcPort); n > maxPortBytes {
			addErr("edge[%d]: src_port exceeds %d bytes (%d)", i, maxPortBytes, n)
		}
		if n := len(e.DstPort); n > maxPortBytes {
			addErr("edge[%d]: dst_port exceeds %d bytes (%d)", i, maxPortBytes, n)
		}
		if n := len(e.DiscoveryProto); n > maxProtoBytes {
			addErr("edge[%d]: discovery_proto exceeds %d bytes (%d)", i, maxProtoBytes, n)
		}
		if n := len(e.LinkKind); n > maxProtoBytes {
			addErr("edge[%d]: link_kind exceeds %d bytes (%d)", i, maxProtoBytes, n)
		}
		for k, v := range e.Metadata {
			if len(errs) >= maxValidationErrors {
				break
			}
			if n := len(k); n > maxLabelKeyBytes {
				addErr("edge[%d]: metadata key exceeds %d bytes (%d)", i, maxLabelKeyBytes, n)
			}
			if n := len(v); n > maxLabelValueBytes {
				addErr("edge[%d]: metadata value for key %q exceeds %d bytes (%d)", i, k, maxLabelValueBytes, n)
			}
		}
	}
	for i, n := range f.OutOfScope {
		if len(errs) >= maxValidationErrors {
			break
		}
		if x := len(n.ReportingDevice); x > maxIDBytes {
			addErr("out_of_scope[%d]: reporting_device exceeds %d bytes (%d)", i, maxIDBytes, x)
		}
		if x := len(n.ReportingPort); x > maxPortBytes {
			addErr("out_of_scope[%d]: reporting_port exceeds %d bytes (%d)", i, maxPortBytes, x)
		}
		if x := len(n.NeighbourHint); x > maxIDBytes {
			addErr("out_of_scope[%d]: neighbour_hint exceeds %d bytes (%d)", i, maxIDBytes, x)
		}
		if x := len(n.Proto); x > maxProtoBytes {
			addErr("out_of_scope[%d]: proto exceeds %d bytes (%d)", i, maxProtoBytes, x)
		}
	}

	if len(errs) >= maxValidationErrors {
		errs = append(errs, fmt.Errorf("and more errors omitted (cap=%d)", maxValidationErrors))
	}
	if len(errs) == 0 {
		return nil
	}
	return errors.Join(errs...)
}
