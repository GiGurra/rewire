package toolexec

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestToolexec_GoWorkspace_ScansAcrossModules drives `go test
// -toolexec=rewire` from the ROOT of a go.work workspace — the exact
// shape retail-mono's CI uses. Two modules live under one go.work:
//
//	tmpDir/
//	  go.work         (uses ./mod-producer ./mod-consumer)
//	  mod-producer/
//	    go.mod        (module example.com/producer)
//	    iface/iface.go  (package iface; Greeter interface)
//	  mod-consumer/
//	    go.mod        (module example.com/consumer; replace-free
//	                   except for the producer via go.work)
//	    app/app_test.go   (rewire.NewMock[iface.Greeter])
//
// Pre-fix, rewire's findModuleInfo walked up from cwd = tmpDir and
// found no go.mod (only go.work) — returned "", "". That blanked the
// moduleRoot passed into scanAllTestFiles, which skipped the walk,
// which meant no NewMock references were ever discovered, which meant
// no registration init was emitted. At test runtime,
// rewire.NewMock[iface.Greeter] threw "no mock factory registered".
//
// Post-fix, findModuleInfo recognises go.work as a valid root marker
// and returns tmpDir, letting the scan discover every _test.go in both
// modules.
func TestToolexec_GoWorkspace_ScansAcrossModules(t *testing.T) {
	ensureRewireInstalled(t)
	tmpDir := t.TempDir()

	producerDir := filepath.Join(tmpDir, "mod-producer")
	if err := os.MkdirAll(filepath.Join(producerDir, "iface"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(producerDir, "go.mod"), []byte(
		"module example.com/producer\n\ngo 1.26.2\n",
	), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(producerDir, "iface", "iface.go"), []byte(
		"package iface\n\ntype Greeter interface{ Greet(name string) string }\n",
	), 0644); err != nil {
		t.Fatal(err)
	}

	consumerDir := filepath.Join(tmpDir, "mod-consumer")
	if err := os.MkdirAll(filepath.Join(consumerDir, "app"), 0755); err != nil {
		t.Fatal(err)
	}
	// Minimal go.mod: no requires. All three modules (producer,
	// consumer, rewire) are resolved via the go.work `use` list
	// below, so no network fetch is needed and we avoid shipping a
	// go.sum for this ephemeral test.
	if err := os.WriteFile(filepath.Join(consumerDir, "go.mod"), []byte(
		"module example.com/consumer\n\ngo 1.26.2\n",
	), 0644); err != nil {
		t.Fatal(err)
	}
	// A test file using NewMock on the producer's interface.
	if err := os.WriteFile(filepath.Join(consumerDir, "app", "app_test.go"), []byte(
		`package app

import (
	"testing"

	"example.com/producer/iface"
	"github.com/GiGurra/rewire/pkg/rewire"
)

func TestWorkspaceNewMock(t *testing.T) {
	g := rewire.NewMock[iface.Greeter](t)
	rewire.InstanceFunc(t, g, iface.Greeter.Greet, func(_ iface.Greeter, name string) string {
		return "hi, " + name
	})
	if got := g.Greet("Alice"); got != "hi, Alice" {
		t.Errorf("got %q, want %q", got, "hi, Alice")
	}
}
`), 0644); err != nil {
		t.Fatal(err)
	}

	// go.work at tmpDir root joining both modules. This is the key
	// detail: tmpDir itself has NO go.mod — findModuleInfo used to
	// walk up and return empty because of that. The scanner needs to
	// recognise go.work and treat tmpDir as a valid scan root.
	// go.work includes rewire itself as a workspace member so the
	// consumer can import github.com/GiGurra/rewire/pkg/rewire
	// without a require/replace in its go.mod.
	if err := os.WriteFile(filepath.Join(tmpDir, "go.work"), []byte(
		"go 1.26.2\n\nuse (\n\t./mod-producer\n\t./mod-consumer\n\t"+mustAbs("../..")+"\n)\n",
	), 0644); err != nil {
		t.Fatal(err)
	}

	testCache := filepath.Join(tmpDir, "gocache")
	var subEnv []string
	for _, e := range os.Environ() {
		// Strip any inherited workspace / cache / flags overrides so
		// the subprocess's view of the world is exactly tmpDir's
		// go.work. GOWORK is the important one here — a parent shell
		// with GOWORK set (or a dev who exports one for their own
		// workspace) would otherwise point go at that file instead
		// of tmpDir/go.work, silently making the test assert against
		// the wrong root.
		if strings.HasPrefix(e, "GOFLAGS=") ||
			strings.HasPrefix(e, "GOCACHE=") ||
			strings.HasPrefix(e, "GOWORK=") {
			continue
		}
		subEnv = append(subEnv, e)
	}
	subEnv = append(subEnv, "GOCACHE="+testCache)

	// Run from the WORKSPACE ROOT with multiple ./mod/... targets in
	// a single invocation — this is the retail-mono CI shape that
	// initially tripped on findModuleInfo. We include the producer
	// even though it has no tests; exercising the multi-target arg
	// list is the point.
	cmd := exec.Command("go", "test", "-toolexec=rewire", "-count=1",
		"./mod-producer/...", "./mod-consumer/...")
	cmd.Dir = tmpDir
	cmd.Env = subEnv
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("workspace-rooted go test failed: %v\n%s", err, out)
	}
}
