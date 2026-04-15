package toolexec

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestIntrinsicFunctionProducesError(t *testing.T) {
	if runtime.GOARCH != "arm64" {
		t.Skip("math.Abs is only a compiler intrinsic on arm64")
	}

	// Create a temp module with a test that tries to mock math.Abs
	tmpDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(
		"module testmod\n\ngo 1.21\n\nrequire github.com/GiGurra/rewire v0.0.0\n\n"+
			"replace github.com/GiGurra/rewire => "+mustAbs("../..")+"\n",
	), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(tmpDir, "pkg"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "pkg", "pkg.go"), []byte(`package pkg

import "math"

func Run() float64 { return math.Abs(-1) }
`), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(tmpDir, "pkg", "pkg_test.go"), []byte(`package pkg

import (
	"math"
	"testing"
	"github.com/GiGurra/rewire/pkg/rewire"
)

func TestAbs(t *testing.T) {
	rewire.Func(t, math.Abs, func(x float64) float64 { return 0 })
}
`), 0644); err != nil {
		t.Fatal(err)
	}

	// Build an environment for subprocesses that:
	// 1. Strips GOFLAGS so the parent's -toolexec=rewire doesn't leak
	//    into go mod tidy or double-apply during go test.
	// 2. Uses an isolated GOCACHE so this test doesn't contaminate the
	//    caller's cache. Without isolation, the subprocess compiles the
	//    entire stdlib through toolexec with the *temp dir* as the module
	//    root — the scan finds no mock targets and stdlib packages are
	//    cached without mock vars, breaking subsequent test runs that
	//    share the same GOCACHE.
	testCache := filepath.Join(tmpDir, "gocache")
	var subEnv []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GOFLAGS=") {
			subEnv = append(subEnv, e)
		}
	}
	subEnv = append(subEnv, "GOCACHE="+testCache)

	// Tidy the module
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = tmpDir
	tidy.Env = subEnv
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy failed: %v\n%s", err, out)
	}

	// Run go test with toolexec — should fail with a clear error
	cmd := exec.Command("go", "test", "-toolexec=rewire", "-count=1", "./pkg/")
	cmd.Dir = tmpDir
	cmd.Env = subEnv
	out, err := cmd.CombinedOutput()
	output := string(out)

	if err == nil {
		t.Fatalf("expected go test to fail, but it succeeded.\nOutput:\n%s", output)
	}

	if !strings.Contains(output, "cannot be mocked") {
		t.Errorf("expected error about function not being mockable.\nOutput:\n%s", output)
	}
	if !strings.Contains(output, "intrinsic") {
		t.Errorf("expected error to mention 'intrinsic'.\nOutput:\n%s", output)
	}
}

// TestNewMockTargetAutoInvalidatesCache exercises the full cycle:
// warm cache → add new mock target → rewire detects + clears cache →
// re-run succeeds.
func TestNewMockTargetAutoInvalidatesCache(t *testing.T) {
	ensureRewireInstalled(t)
	tmpDir := t.TempDir()
	testCache := filepath.Join(tmpDir, "gocache")
	pkgDir := filepath.Join(tmpDir, "pkg")

	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatal(err)
	}

	// go.mod with a replace pointing at the local rewire checkout.
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(
		"module testmod\n\ngo 1.21\n\nrequire github.com/GiGurra/rewire v0.0.0\n\n"+
			"replace github.com/GiGurra/rewire => "+mustAbs("../..")+"\n",
	), 0644); err != nil {
		t.Fatal(err)
	}

	// Production code that calls os.Getwd and os.Hostname.
	if err := os.WriteFile(filepath.Join(pkgDir, "pkg.go"), []byte(`package pkg

import "os"

func Dir() string  { d, _ := os.Getwd(); return d }
func Host() string { h, _ := os.Hostname(); return h }
`), 0644); err != nil {
		t.Fatal(err)
	}

	// Initial test: only mocks os.Getwd.
	testV1 := []byte(`package pkg

import (
	"os"
	"testing"
	"github.com/GiGurra/rewire/pkg/rewire"
)

func TestGetwd(t *testing.T) {
	rewire.Func(t, os.Getwd, func() (string, error) { return "/mock", nil })
	if got := Dir(); got != "/mock" {
		t.Fatalf("got %q, want /mock", got)
	}
}
`)
	if err := os.WriteFile(filepath.Join(pkgDir, "pkg_test.go"), testV1, 0644); err != nil {
		t.Fatal(err)
	}

	// Build a subprocess environment: isolated GOCACHE, no GOFLAGS.
	var subEnv []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GOFLAGS=") {
			subEnv = append(subEnv, e)
		}
	}
	subEnv = append(subEnv, "GOCACHE="+testCache)

	// Tidy
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = tmpDir
	tidy.Env = subEnv
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy: %v\n%s", err, out)
	}

	run := func(label string) (string, error) {
		cmd := exec.Command("go", "test", "-toolexec=rewire", "-count=1", "./pkg/")
		cmd.Dir = tmpDir
		cmd.Env = subEnv
		out, err := cmd.CombinedOutput()
		t.Logf("[%s] output:\n%s", label, out)
		return string(out), err
	}

	// Step 1: initial run — should pass, warms the cache.
	if _, err := run("initial"); err != nil {
		t.Fatalf("initial run failed: %v", err)
	}

	// Step 2: add os.Hostname mock to the test file.
	testV2 := []byte(`package pkg

import (
	"os"
	"testing"
	"github.com/GiGurra/rewire/pkg/rewire"
)

func TestGetwd(t *testing.T) {
	rewire.Func(t, os.Getwd, func() (string, error) { return "/mock", nil })
	if got := Dir(); got != "/mock" {
		t.Fatalf("got %q, want /mock", got)
	}
}

func TestHostname(t *testing.T) {
	rewire.Func(t, os.Hostname, func() (string, error) { return "mockhost", nil })
	if got := Host(); got != "mockhost" {
		t.Fatalf("got %q, want mockhost", got)
	}
}
`)
	if err := os.WriteFile(filepath.Join(pkgDir, "pkg_test.go"), testV2, 0644); err != nil {
		t.Fatal(err)
	}

	// Step 3: first run after adding new target — should detect the
	// change, clear cache, and fail with a helpful message.
	out, err := run("after-new-target")
	if err == nil {
		t.Fatal("expected failure after adding new mock target, but test passed")
	}
	if !strings.Contains(out, "mock target set changed") {
		t.Errorf("expected 'mock target set changed' message, got:\n%s", out)
	}

	// Step 4: re-run — cache was cleared, should rebuild and pass.
	if _, err := run("re-run"); err != nil {
		t.Fatalf("re-run after cache clear failed: %v", err)
	}

	// Step 5: one more run — should be cached and still pass.
	if _, err := run("cached"); err != nil {
		t.Fatalf("cached run failed: %v", err)
	}
}

// ensureRewireInstalled builds and installs the rewire binary if it
// isn't already in $PATH. Integration tests that shell out to
// `go test -toolexec=rewire` need the binary available.
func ensureRewireInstalled(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("rewire"); err == nil {
		return // already installed
	}
	cmd := exec.Command("go", "install", "../../cmd/rewire/")
	cmd.Env = envWithoutGOFLAGS()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to install rewire: %v\n%s", err, out)
	}
}

func mustAbs(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		panic(err)
	}
	return abs
}
