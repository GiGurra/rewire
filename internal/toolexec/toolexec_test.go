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

func mustAbs(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		panic(err)
	}
	return abs
}
