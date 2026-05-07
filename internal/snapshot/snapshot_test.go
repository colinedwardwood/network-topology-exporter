package snapshot

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/colinedwardwood/network-topology-exporter/internal/discovery"
)

// errInjected is a sentinel used by injection helpers to distinguish injected
// failures from real OS errors in assertions.
var errInjected = errors.New("injected failure")

// restoreOSFns returns a function that restores all injectable OS-call vars to
// their current values. Call it with defer at the top of any test that
// overrides these vars.
func restoreOSFns() func() {
	origReadFile := readFileFn
	origMarshal := marshalFn
	origMkdirAll := mkdirAllFn
	origCreateTemp := createTempFn
	origRename := renameFn
	origOpenFile := openFileFn
	return func() {
		readFileFn = origReadFile
		marshalFn = origMarshal
		mkdirAllFn = origMkdirAll
		createTempFn = origCreateTemp
		renameFn = origRename
		openFileFn = origOpenFile
	}
}

// fakeTmpFile wraps a real *os.File but lets individual operations be
// overridden by the test. Fields left nil delegate to the real file.
type fakeTmpFile struct {
	real    *os.File
	writeFn func([]byte) (int, error)
	syncFn  func() error
	closeFn func() error
}

func (f *fakeTmpFile) Name() string { return f.real.Name() }
func (f *fakeTmpFile) Write(b []byte) (int, error) {
	if f.writeFn != nil {
		return f.writeFn(b)
	}
	return f.real.Write(b)
}
func (f *fakeTmpFile) Sync() error {
	if f.syncFn != nil {
		return f.syncFn()
	}
	return f.real.Sync()
}
func (f *fakeTmpFile) Close() error {
	if f.closeFn != nil {
		return f.closeFn()
	}
	return f.real.Close()
}

// fakeQuarantineFile is a quarantineFile implementation whose operations can
// be overridden to inject errors. The real file underneath is only used to
// satisfy the interface; it is always closed regardless of injected errors.
type fakeQuarantineFile struct {
	real    *os.File
	writeFn func([]byte) (int, error)
	closeFn func() error
}

func (f *fakeQuarantineFile) Write(b []byte) (int, error) {
	if f.writeFn != nil {
		return f.writeFn(b)
	}
	return f.real.Write(b)
}
func (f *fakeQuarantineFile) Close() error {
	_ = f.real.Close() // always close the real file to avoid leaks
	if f.closeFn != nil {
		return f.closeFn()
	}
	return nil
}

// LD-13: a missing snapshot is "first run", not an error. Operators don't
// need to pre-create the file.
func TestLoadMissingFileReturnsNil(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "missing.json"))
	if err != nil {
		t.Fatalf("missing snapshot should not error: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil File for missing path, got %#v", got)
	}
}

// LD-13: round-trip a snapshot through Write+Load and confirm the device,
// edge, and credential-cache contents survive verbatim.
func TestWriteLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snap.json")
	in := File{
		Devices: []discovery.Device{{ID: "dev-1", Vendor: "cisco", Site: "lab"}},
		Edges: []discovery.Edge{{
			SrcDevice: "dev-1", SrcPort: "Gi0/1",
			DstDevice: "dev-2", DstPort: "Gi0/2",
			DiscoveryProto: "lldp",
			Direction:      discovery.DirectionBidirectional,
			Confidence:     discovery.ConfidenceHigh,
			Adjacency:      discovery.AdjacencyDirect,
			PrecedenceRank: 2,
		}},
		CredentialCache: map[string]string{"dev-1": "core-v3"},
		UnconfirmedAges: map[string]int{"a|Gi0/1|b|Gi0/2": 1},
	}
	if err := Write(path, in); err != nil {
		t.Fatalf("Write: %v", err)
	}

	out, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out == nil {
		t.Fatal("Load returned nil for an existing snapshot")
	}
	if out.Version != CurrentVersion {
		t.Errorf("Version = %d, want %d", out.Version, CurrentVersion)
	}
	if len(out.Devices) != 1 || out.Devices[0].ID != "dev-1" {
		t.Errorf("Devices round-trip mismatch: %#v", out.Devices)
	}
	if len(out.Edges) != 1 || out.Edges[0].PrecedenceRank != 2 {
		t.Errorf("Edges round-trip mismatch: %#v", out.Edges)
	}
	if out.CredentialCache["dev-1"] != "core-v3" {
		t.Errorf("CredentialCache round-trip mismatch: %#v", out.CredentialCache)
	}
}

// LD-13: a snapshot with an unrecognised version is not silently parsed.
// Falling through to a cold start is documented behaviour, but the caller
// has to see the version-mismatch error to log the warning first.
func TestLoadRejectsUnknownVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snap.json")
	body := []byte(`{"version": 99, "devices": []}`)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("Load error = %v, want ErrVersionMismatch", err)
	}
}

// LD-13: Load returns an error (not nil) for a file that exists but is not
// valid JSON.
func TestLoadCorruptJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error for corrupt JSON, got nil")
	}
}

func TestLoadCorruptJSONDoesNotOverwriteExistingBadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "corrupt.json")
	badPath := path + ".bad"
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write corrupt snapshot: %v", err)
	}
	if err := os.WriteFile(badPath, []byte("previous bad snapshot"), 0o600); err != nil {
		t.Fatalf("write existing bad snapshot: %v", err)
	}

	if _, err := Load(path); err == nil {
		t.Fatal("expected error for corrupt JSON, got nil")
	}

	existing, err := os.ReadFile(badPath) //nolint:gosec
	if err != nil {
		t.Fatalf("read existing bad snapshot: %v", err)
	}
	if string(existing) != "previous bad snapshot" {
		t.Fatalf("%s was overwritten with %q", badPath, string(existing))
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	foundQuarantine := false
	for _, e := range entries {
		if e.Name() != "corrupt.json.bad" && strings.HasPrefix(e.Name(), "corrupt.json.bad.") {
			foundQuarantine = true
			break
		}
	}
	if !foundQuarantine {
		t.Fatalf("expected corrupt snapshot to be moved to a unique .bad path, entries: %v", entries)
	}
}

// LD-13: Write succeeds even when the caller does not set Version (defaults
// to CurrentVersion) and WrittenAt (defaults to now).
func TestWriteDefaultsVersionAndTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snap.json")
	f := File{} // zero value
	if err := Write(path, f); err != nil {
		t.Fatalf("Write: %v", err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.Version != CurrentVersion {
		t.Errorf("Version = %d, want %d", out.Version, CurrentVersion)
	}
	if out.WrittenAt.IsZero() {
		t.Error("WrittenAt is zero, want a non-zero timestamp")
	}
}

// LD-13: Write returns an error when the directory cannot be created because
// the target parent path is occupied by a regular file.
func TestWriteFailsWhenParentIsFile(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(parent, []byte("x"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	path := filepath.Join(parent, "snap.json")
	if err := Write(path, File{}); err == nil {
		t.Error("expected error when parent is a file, got nil")
	}
}

// TestQuarantineRollsToNextAvailableSuffix verifies that when both .bad and
// .bad.1 already exist the quarantine function advances to .bad.2.
func TestQuarantineRollsToNextAvailableSuffix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snap.json")

	// Pre-create both .bad and .bad.1 so quarantine must use .bad.2.
	for _, suffix := range []string{".bad", ".bad.1"} {
		if err := os.WriteFile(path+suffix, []byte("old"), 0o600); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	if err := os.WriteFile(path, []byte("{bad json"), 0o600); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}

	_, err := Load(path)
	if err == nil {
		t.Fatal("expected error for corrupt JSON, got nil")
	}

	if _, err := os.Stat(path + ".bad.2"); err != nil {
		t.Fatalf("expected .bad.2 to exist: %v", err)
	}
	// Original .bad and .bad.1 must still be the old content.
	for _, suffix := range []string{".bad", ".bad.1"} {
		b, _ := os.ReadFile(path + suffix) //nolint:gosec
		if string(b) != "old" {
			t.Errorf("%s was overwritten; want %q, got %q", suffix, "old", string(b))
		}
	}
}

// TestWriteCreatesDirectoryIfAbsent verifies that Write creates intermediate
// directories (MkdirAll behaviour) when the directory does not yet exist.
func TestWriteCreatesDirectoryIfAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "snap.json")
	if err := Write(path, File{}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := Load(path); err != nil {
		t.Fatalf("Load after Write into new dir: %v", err)
	}
}

// TestLoadVersionMismatchQuarantinesFile confirms that a version mismatch
// moves the file to .bad rather than deleting it silently.
func TestLoadVersionMismatchQuarantinesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snap.json")
	if err := os.WriteFile(path, []byte(`{"version":99}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := Load(path)
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("error = %v, want ErrVersionMismatch", err)
	}

	// The original file should be gone (renamed to .bad).
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("original snap.json should have been quarantined (renamed away)")
	}
	if _, statErr := os.Stat(path + ".bad"); statErr != nil {
		t.Errorf(".bad file should exist after version mismatch: %v", statErr)
	}
}

// LD-13: a crash mid-write must leave the previous snapshot intact and the
// .tmp file behind. We can't simulate a crash, but we can verify the path
// is updated atomically by writing twice and confirming the second write
// supersedes the first.
func TestWriteIsAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snap.json")
	v1 := File{Devices: []discovery.Device{{ID: "dev-v1"}}}
	v2 := File{Devices: []discovery.Device{{ID: "dev-v2"}}}

	if err := Write(path, v1); err != nil {
		t.Fatalf("Write v1: %v", err)
	}
	if err := Write(path, v2); err != nil {
		t.Fatalf("Write v2: %v", err)
	}

	out, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.Devices[0].ID != "dev-v2" {
		t.Errorf("second write did not supersede first: %#v", out.Devices)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("temp file %q should have been renamed away", e.Name())
		}
	}
}

// ---------------------------------------------------------------------------
// Load error-path tests
// ---------------------------------------------------------------------------

// TestLoadReadError covers the branch where os.ReadFile returns a non-ErrNotExist
// error (e.g. permission denied). Load must propagate the error.
func TestLoadReadError(t *testing.T) {
	defer restoreOSFns()()
	readFileFn = func(string) ([]byte, error) { return nil, errInjected }

	_, err := Load("/any/path.json")
	if err == nil {
		t.Fatal("expected error from injected readFileFn, got nil")
	}
	if !errors.Is(err, errInjected) {
		t.Errorf("error = %v, want to wrap errInjected", err)
	}
}

// TestLoadQuarantinesVersionMismatch confirms that when Load encounters a
// version mismatch it quarantines the file (original path removed, .bad
// created) and returns ErrVersionMismatch.
func TestLoadQuarantinesVersionMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snap.json")
	if err := os.WriteFile(path, []byte(`{"version":99}`), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, err := Load(path)
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("error = %v, want ErrVersionMismatch", err)
	}

	// Original must be gone.
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("original file should have been quarantined (removed)")
	}
	// Quarantine file must exist.
	if _, statErr := os.Stat(path + ".bad"); statErr != nil {
		t.Errorf(".bad file should exist after version mismatch: %v", statErr)
	}
}

// ---------------------------------------------------------------------------
// quarantine error-path tests
// ---------------------------------------------------------------------------

// TestQuarantineRollsSuffixOnConflict verifies that when the .bad path already
// exists quarantine advances to the next available suffix (.bad.1, .bad.2, …).
func TestQuarantineRollsSuffixOnConflict(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "snap.json")

	// Pre-create .bad so the first suffix is taken.
	if err := os.WriteFile(path+".bad", []byte("existing"), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"version":99}`), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}

	_, err := Load(path)
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("error = %v, want ErrVersionMismatch", err)
	}

	// .bad must still have the old content.
	b, _ := os.ReadFile(path + ".bad") //nolint:gosec
	if string(b) != "existing" {
		t.Errorf(".bad content = %q, want %q", string(b), "existing")
	}
	// A new suffix must have been created.
	if _, statErr := os.Stat(path + ".bad.1"); statErr != nil {
		t.Errorf(".bad.1 should exist after suffix roll: %v", statErr)
	}
}

// TestQuarantineOpenError exercises the branch where os.OpenFile fails for a
// reason other than ErrExist (e.g. permission denied). quarantine returns the
// error; Load surfaces it silently (the result is discarded with _).
func TestQuarantineOpenError(t *testing.T) {
	defer restoreOSFns()()
	openFileFn = func(string, int, fs.FileMode) (quarantineFile, error) {
		return nil, errInjected
	}

	// quarantine is called inside Load for a version mismatch; the returned
	// error is ignored by Load, but the function still returns ErrVersionMismatch.
	dir := t.TempDir()
	path := filepath.Join(dir, "snap.json")
	if err := os.WriteFile(path, []byte(`{"version":99}`), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := Load(path)
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("error = %v, want ErrVersionMismatch", err)
	}
}

// TestQuarantineWriteError exercises the branch where writing to the .bad file
// fails. quarantine returns the write error.
func TestQuarantineWriteError(t *testing.T) {
	defer restoreOSFns()()
	openFileFn = func(name string, flag int, perm fs.FileMode) (quarantineFile, error) {
		f, err := os.OpenFile(name, flag, perm) //nolint:gosec
		if err != nil {
			return nil, err
		}
		return &fakeQuarantineFile{
			real:    f,
			writeFn: func([]byte) (int, error) { return 0, errInjected },
		}, nil
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "snap.json")
	if err := os.WriteFile(path, []byte(`{"version":99}`), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// Load ignores the quarantine error; we just verify it doesn't panic and
	// returns ErrVersionMismatch.
	_, err := Load(path)
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("error = %v, want ErrVersionMismatch", err)
	}
}

// TestQuarantineCloseError exercises the branch where closing the .bad file
// fails. quarantine returns the close error and removes the partial file.
func TestQuarantineCloseError(t *testing.T) {
	defer restoreOSFns()()
	openFileFn = func(name string, flag int, perm fs.FileMode) (quarantineFile, error) {
		f, err := os.OpenFile(name, flag, perm) //nolint:gosec
		if err != nil {
			return nil, err
		}
		return &fakeQuarantineFile{
			real:    f,
			closeFn: func() error { return errInjected },
		}, nil
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "snap.json")
	if err := os.WriteFile(path, []byte(`{"version":99}`), 0o600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	_, err := Load(path)
	if !errors.Is(err, ErrVersionMismatch) {
		t.Fatalf("error = %v, want ErrVersionMismatch", err)
	}
}

// ---------------------------------------------------------------------------
// Write error-path tests
// ---------------------------------------------------------------------------

// TestWriteMarshalError injects a marshal failure so the json.Marshal branch
// in Write is exercised.
func TestWriteMarshalError(t *testing.T) {
	defer restoreOSFns()()
	marshalFn = func(any) ([]byte, error) { return nil, errInjected }

	err := Write(filepath.Join(t.TempDir(), "snap.json"), File{})
	if err == nil {
		t.Fatal("expected error from injected marshalFn, got nil")
	}
	if !errors.Is(err, errInjected) {
		t.Errorf("error = %v, want to wrap errInjected", err)
	}
}

// TestWriteMkdirAllError injects an os.MkdirAll failure.
func TestWriteMkdirAllError(t *testing.T) {
	defer restoreOSFns()()
	mkdirAllFn = func(string, fs.FileMode) error { return errInjected }

	err := Write(filepath.Join(t.TempDir(), "snap.json"), File{})
	if err == nil {
		t.Fatal("expected error from injected mkdirAllFn, got nil")
	}
	if !errors.Is(err, errInjected) {
		t.Errorf("error = %v, want to wrap errInjected", err)
	}
}

// TestWriteCreateTempError injects an os.CreateTemp failure.
func TestWriteCreateTempError(t *testing.T) {
	defer restoreOSFns()()
	createTempFn = func(string, string) (tmpFile, error) { return nil, errInjected }

	err := Write(filepath.Join(t.TempDir(), "snap.json"), File{})
	if err == nil {
		t.Fatal("expected error from injected createTempFn, got nil")
	}
	if !errors.Is(err, errInjected) {
		t.Errorf("error = %v, want to wrap errInjected", err)
	}
}

// TestWriteTempWriteError injects a failure on the Write call to the temp file.
func TestWriteTempWriteError(t *testing.T) {
	defer restoreOSFns()()
	dir := t.TempDir()
	createTempFn = func(d, pat string) (tmpFile, error) {
		f, err := os.CreateTemp(d, pat)
		if err != nil {
			return nil, err
		}
		return &fakeTmpFile{
			real:    f,
			writeFn: func([]byte) (int, error) { return 0, errInjected },
		}, nil
	}

	err := Write(filepath.Join(dir, "snap.json"), File{})
	if err == nil {
		t.Fatal("expected error from injected write, got nil")
	}
	if !errors.Is(err, errInjected) {
		t.Errorf("error = %v, want to wrap errInjected", err)
	}
}

// TestWriteSyncError injects a failure on the Sync call to the temp file.
func TestWriteSyncError(t *testing.T) {
	defer restoreOSFns()()
	dir := t.TempDir()
	createTempFn = func(d, pat string) (tmpFile, error) {
		f, err := os.CreateTemp(d, pat)
		if err != nil {
			return nil, err
		}
		return &fakeTmpFile{
			real:   f,
			syncFn: func() error { return errInjected },
		}, nil
	}

	err := Write(filepath.Join(dir, "snap.json"), File{})
	if err == nil {
		t.Fatal("expected error from injected sync, got nil")
	}
	if !errors.Is(err, errInjected) {
		t.Errorf("error = %v, want to wrap errInjected", err)
	}
}

// TestWriteCloseError injects a failure on the Close call to the temp file.
func TestWriteCloseError(t *testing.T) {
	defer restoreOSFns()()
	dir := t.TempDir()
	createTempFn = func(d, pat string) (tmpFile, error) {
		f, err := os.CreateTemp(d, pat)
		if err != nil {
			return nil, err
		}
		return &fakeTmpFile{
			real:    f,
			closeFn: func() error { return errInjected },
		}, nil
	}

	err := Write(filepath.Join(dir, "snap.json"), File{})
	if err == nil {
		t.Fatal("expected error from injected close, got nil")
	}
	if !errors.Is(err, errInjected) {
		t.Errorf("error = %v, want to wrap errInjected", err)
	}
}

// TestWriteRenameError injects an os.Rename failure.
func TestWriteRenameError(t *testing.T) {
	defer restoreOSFns()()
	renameFn = func(string, string) error { return errInjected }

	err := Write(filepath.Join(t.TempDir(), "snap.json"), File{})
	if err == nil {
		t.Fatal("expected error from injected renameFn, got nil")
	}
	if !errors.Is(err, errInjected) {
		t.Errorf("error = %v, want to wrap errInjected", err)
	}
}
