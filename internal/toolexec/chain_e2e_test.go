package toolexec

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestChainAndThen_InvokesSuccessor drives a real `go test -toolexec`
// chain through a sentinel-writing shim so the test can prove TWO
// things:
//
//  1. The mocked test passes (mock wiring survives the chain).
//  2. The successor actually ran (sentinel file non-empty after
//     build). Without this, a regression where rewire silently
//     dropped the successor on some paths would still make the
//     test pass, because rewire's rewriting alone is enough for
//     the mock to work.
//
// The shim is a tiny POSIX shell script: appends "$1" (the tool path)
// to a log, then execs "$@" — i.e. it's a passthrough with a side
// effect we can observe.
func TestChainAndThen_InvokesSuccessor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shim is sh-based")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("no sh on PATH: %v", err)
	}

	ensureRewireInstalled(t)

	tmpDir := t.TempDir()
	pkgDir := filepath.Join(tmpDir, "pkg")
	sentinelPath := filepath.Join(tmpDir, "chain.log")
	shimPath := filepath.Join(tmpDir, "shim.sh")

	if err := os.MkdirAll(pkgDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Shim: log the tool path, then exec the remaining argv. The
	// shell expands "$@" correctly even when the rewritten .go
	// paths contain spaces.
	shim := "#!/bin/sh\nset -eu\nprintf '%s\\n' \"$1\" >> " + sentinelPath + "\nexec \"$@\"\n"
	if err := os.WriteFile(shimPath, []byte(shim), 0755); err != nil {
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

	// -toolexec invokes "rewire --and-then <shim>" as a single program
	// string. Go tokenizes it and prepends it to every tool call.
	// rewire parses its own args up to --and-then, treats the rest
	// as "the command that replaces the bare tool invocation".
	toolexec := "rewire --and-then " + shimPath
	cmd := exec.Command("go", "test", "-toolexec="+toolexec, "-count=1", "./pkg/")
	cmd.Dir = tmpDir
	cmd.Env = subEnv
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("chained go test failed: %v\n%s", err, out)
	}

	data, err := os.ReadFile(sentinelPath)
	if err != nil {
		t.Fatalf("sentinel not written — shim never invoked, so the chain dropped the successor: %v", err)
	}
	log := strings.TrimSpace(string(data))
	if log == "" {
		t.Fatal("sentinel empty — shim ran but logged nothing; unexpected")
	}
	// The shim sees the go-tool path as its first arg on every
	// invocation. A successful build under -toolexec hits compile
	// at minimum, so the log must mention it.
	if !strings.Contains(log, string(os.PathSeparator)+"compile") {
		t.Fatalf("sentinel log does not mention the compile tool — chain wiring suspect.\nlog:\n%s", log)
	}
}
