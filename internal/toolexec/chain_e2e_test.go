package toolexec

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestChainAndThen_ViaEnvPassthrough runs a real `go test -toolexec`
// invocation with `rewire --and-then env <compile>` and verifies the
// test still passes. `env`, given a command as its args, simply execs
// that command — so the chain degenerates to "rewire rewrites, env
// forwards to compile". If the chain wiring is wrong (e.g. rewire
// bypasses rewriting when it sees a non-compile tool, or the
// successor is dropped at exec), the mocked test would fail or the
// compile would blow up with "undefined: _rewire_*".
//
// This is the minimum viable e2e: one real preprocessor plus one
// trivial passthrough. Multi-preprocessor chains use the same
// mechanism, so if this passes, N-hop chains with rewire at the
// front are wired correctly.
func TestChainAndThen_ViaEnvPassthrough(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no /usr/bin/env on Windows")
	}
	envPath, err := exec.LookPath("env")
	if err != nil {
		t.Skipf("no `env` binary on PATH: %v", err)
	}

	ensureRewireInstalled(t)

	tmpDir := t.TempDir()
	pkgDir := filepath.Join(tmpDir, "pkg")
	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(
		"module testmod\n\ngo 1.21\n\nrequire github.com/GiGurra/rewire v0.0.0\n\n"+
			"replace github.com/GiGurra/rewire => "+mustAbs("../..")+"\n",
	), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(pkgDir, "pkg.go"), []byte(`package pkg

import "os"

func Dir() string { d, _ := os.Getwd(); return d }
`), 0644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(pkgDir, "pkg_test.go"), []byte(`package pkg

import (
	"os"
	"testing"
	"github.com/GiGurra/rewire/pkg/rewire"
)

func TestGetwd(t *testing.T) {
	rewire.Func(t, os.Getwd, func() (string, error) { return "/chained", nil })
	if got := Dir(); got != "/chained" {
		t.Fatalf("got %q, want /chained", got)
	}
}
`), 0644); err != nil {
		t.Fatal(err)
	}

	testCache := filepath.Join(tmpDir, "gocache")
	var subEnv []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "GOFLAGS=") || strings.HasPrefix(e, "GOCACHE=") {
			continue
		}
		subEnv = append(subEnv, e)
	}
	subEnv = append(subEnv, "GOCACHE="+testCache)

	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = tmpDir
	tidy.Env = subEnv
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy: %v\n%s", err, out)
	}

	// -toolexec invokes "rewire --and-then env" as a single program
	// string. Go tokenizes it and prepends it to every tool call.
	// rewire parses its own args up to --and-then, treats the rest
	// as "the command that replaces the bare tool invocation".
	toolexec := "rewire --and-then " + envPath
	cmd := exec.Command("go", "test", "-toolexec="+toolexec, "-count=1", "./pkg/")
	cmd.Dir = tmpDir
	cmd.Env = subEnv
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("chained go test failed: %v\n%s", err, out)
	}
}
