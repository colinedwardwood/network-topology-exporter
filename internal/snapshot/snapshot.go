// Package snapshot implements LD-13: versioned JSON persistence of the
// reconciled graph plus the LD-12 credential cache. The exporter loads the
// snapshot at startup so /metrics serves the previous-cycle graph immediately
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
const CurrentVersion = 1

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
		_ = quarantine(path, b)
		return nil, fmt.Errorf("parse snapshot %q: %w", path, err)
	}
	if f.Version != CurrentVersion {
		_ = quarantine(path, b)
		return nil, fmt.Errorf("%w: got %d, want %d", ErrVersionMismatch, f.Version, CurrentVersion)
	}
	return &f, nil
}

func quarantine(path string, contents []byte) error {
	for i := 0; ; i++ {
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
