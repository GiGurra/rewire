package toolexec

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Sample scan cache contents used across the round-trip tests.
func sampleCache(header cacheHeader) scanCache {
	return scanCache{
		Header: header,
		Targets: mockTargets{
			"example/bar": []string{"Greet"},
		},
		Instantiations: genericInstantiations{
			"example/bar": {"Map": {{"int", "string"}}},
		},
		ByInstance: byInstanceTargets{
			"example/bar": {"(*Server).Handle": true},
		},
		MockedInterfaces: mockedInterfaces{
			"example/bar": {"GreeterIface": []mockInstance{{TypeArgs: nil}}},
		},
	}
}

func TestReadScanCacheFile_RoundTripWithMatchingHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mock_targets.json")
	header := cacheHeader{ParentPID: 1234, ParentStartTime: 5678, ModuleRoot: "/proj/a"}
	writeScanCacheFile(path, sampleCache(header))

	got, ok := readScanCacheFile(path, header)
	if !ok {
		t.Fatalf("expected cache hit, got miss")
	}
	want := sampleCache(header)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", got, want)
	}
}

func TestReadScanCacheFile_RejectsMismatchedPID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mock_targets.json")
	writer := cacheHeader{ParentPID: 1234, ParentStartTime: 5678, ModuleRoot: "/proj/a"}
	writeScanCacheFile(path, sampleCache(writer))

	reader := writer
	reader.ParentPID = 4321
	if _, ok := readScanCacheFile(path, reader); ok {
		t.Errorf("expected miss on mismatched ParentPID, got hit")
	}
}

func TestReadScanCacheFile_RejectsMismatchedStartTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mock_targets.json")
	writer := cacheHeader{ParentPID: 1234, ParentStartTime: 5678, ModuleRoot: "/proj/a"}
	writeScanCacheFile(path, sampleCache(writer))

	reader := writer
	reader.ParentStartTime = 9999
	if _, ok := readScanCacheFile(path, reader); ok {
		t.Errorf("expected miss on mismatched ParentStartTime, got hit")
	}
}

func TestReadScanCacheFile_RejectsMismatchedModuleRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mock_targets.json")
	writer := cacheHeader{ParentPID: 1234, ParentStartTime: 5678, ModuleRoot: "/proj/a"}
	writeScanCacheFile(path, sampleCache(writer))

	reader := writer
	reader.ModuleRoot = "/proj/b"
	if _, ok := readScanCacheFile(path, reader); ok {
		t.Errorf("expected miss on mismatched ModuleRoot, got hit")
	}
}

// Caches written by an older rewire version won't have the header
// field. Such caches must be rejected rather than inherited — their
// identity can't be verified.
func TestReadScanCacheFile_RejectsOldFormatWithoutHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mock_targets.json")
	oldFormat := []byte(`{"targets":{"example/bar":["Greet"]}}`)
	if err := os.WriteFile(path, oldFormat, 0644); err != nil {
		t.Fatal(err)
	}

	want := cacheHeader{ParentPID: 1234, ParentStartTime: 5678, ModuleRoot: "/proj/a"}
	if _, ok := readScanCacheFile(path, want); ok {
		t.Errorf("expected miss on old-format cache without header, got hit")
	}
}

func TestReadScanCacheFile_MissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.json")
	want := cacheHeader{ParentPID: 1234, ParentStartTime: 5678, ModuleRoot: "/proj/a"}
	if _, ok := readScanCacheFile(path, want); ok {
		t.Errorf("expected miss on missing file, got hit")
	}
}

func TestReadScanCacheFile_MalformedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mock_targets.json")
	if err := os.WriteFile(path, []byte(`{not valid json`), 0644); err != nil {
		t.Fatal(err)
	}

	want := cacheHeader{ParentPID: 1234, ParentStartTime: 5678, ModuleRoot: "/proj/a"}
	if _, ok := readScanCacheFile(path, want); ok {
		t.Errorf("expected miss on malformed JSON, got hit")
	}
}

// A cache whose header matches but whose Targets is nil is rejected
// before we even look at the header — the nil-Targets guard in
// readScanCacheFile catches caches that decoded successfully but
// carry no useful payload (e.g. a file truncated after the header
// was written, or a future format where Targets was moved under a
// different key). Without this branch, such a cache would return
// ok=true with a nil map and the caller would mistake empty-targets
// for "no rewire calls found in the module."
func TestReadScanCacheFile_RejectsNilTargets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mock_targets.json")
	header := cacheHeader{ParentPID: 1234, ParentStartTime: 5678, ModuleRoot: "/proj/a"}
	headerOnly := []byte(`{"header":{"parentPid":1234,"parentStartTime":5678,"moduleRoot":"/proj/a"}}`)
	if err := os.WriteFile(path, headerOnly, 0644); err != nil {
		t.Fatal(err)
	}

	if _, ok := readScanCacheFile(path, header); ok {
		t.Errorf("expected miss on cache with matching header but nil Targets, got hit")
	}
}
