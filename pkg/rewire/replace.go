package rewire

import (
	"fmt"
	"os"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
)

var registry sync.Map // func name (string) → mock var pointer (any)

// Register maps a fully-qualified function name to a pointer to its mock variable.
// This is called by generated init() code — users should not call it directly.
func Register(funcName string, mockVarPtr any) {
	registry.Store(funcName, mockVarPtr)
}

// Func replaces the implementation of original with replacement for the duration
// of the test. The original function is automatically restored via t.Cleanup.
//
// Requires -toolexec=rewire (or GOFLAGS="-toolexec=rewire") to be active.
//
// Usage:
//
//	rewire.Func(t, bar.Greet, func(name string) string {
//	    return "mocked"
//	})
func Func[F any](t *testing.T, original F, replacement F) {
	t.Helper()

	elemVal := resolveMockVar(t, original)

	// Save current value (may be nil)
	oldVal := reflect.New(elemVal.Type()).Elem()
	oldVal.Set(elemVal)

	// Set replacement
	elemVal.Set(reflect.ValueOf(replacement))

	t.Cleanup(func() {
		elemVal.Set(oldVal)
	})
}

// Restore clears any active mock for original, so subsequent calls in the
// same test use the real implementation. Restore is optional — the test's
// automatic cleanup already restores mocks at test end — but it lets you
// end a mock mid-test.
//
// Restore is idempotent and safe to call any number of times. The automatic
// cleanup installed by Func still runs correctly afterwards.
func Restore[F any](t *testing.T, original F) {
	t.Helper()

	elemVal := resolveMockVar(t, original)
	elemVal.Set(reflect.Zero(elemVal.Type()))
}

// resolveMockVar returns a reflect.Value addressing the package-level mock
// variable for original, or fatally fails the test with a targeted
// diagnostic if the function cannot be mocked.
func resolveMockVar[F any](t *testing.T, original F) reflect.Value {
	t.Helper()

	name := funcName(original)

	if msg := methodValueError(name); msg != "" {
		t.Fatal(msg)
		return reflect.Value{}
	}

	mockPtrAny, ok := registry.Load(name)
	if !ok {
		goflags := os.Getenv("GOFLAGS")
		if strings.Contains(goflags, "-toolexec=rewire") {
			t.Fatalf("rewire: function %s cannot be mocked.\n"+
				"  The toolexec is active but the function was not found in any source file.\n"+
				"  Possible causes:\n"+
				"    - The function is a compiler intrinsic (e.g. math.Abs, math.Sqrt on arm64)\n"+
				"    - The function is implemented in assembly\n"+
				"    - The function name is misspelled",
				name)
		} else {
			if goflags == "" {
				goflags = "(not set)"
			}
			t.Fatalf("rewire: function %s cannot be mocked — toolexec rewriting is not active.\n"+
				"  Current GOFLAGS=%s\n"+
				"  To fix:\n"+
				"    1. Set GOFLAGS to include -toolexec=rewire (e.g. export GOFLAGS=\"-toolexec=rewire\")\n"+
				"    2. Run: go clean -cache\n"+
				"    3. Re-run your tests",
				name, goflags)
		}
		return reflect.Value{}
	}

	return reflect.ValueOf(mockPtrAny).Elem()
}

// Replace swaps the value at target with replacement for the duration of the test.
// This is the low-level API — prefer Func for a cleaner experience.
func Replace[F any](t *testing.T, target *F, replacement F) {
	t.Helper()
	old := *target
	*target = replacement
	t.Cleanup(func() { *target = old })
}

// methodValueError returns a targeted error message if name looks like a
// method value (has a "-fm" suffix produced by runtime.FuncForPC), or ""
// otherwise. Method values are unsupported because method mocks are global:
// the mock replaces the method for all instances of the type, so binding to
// a specific instance is misleading. Users must pass a method expression
// such as (*pkg.Type).Method instead.
func methodValueError(name string) string {
	if !strings.HasSuffix(name, "-fm") {
		return ""
	}
	cleaned := strings.TrimSuffix(name, "-fm")
	return fmt.Sprintf(
		"rewire: %s looks like a method value (e.g. instance.Method) — rewire needs a method expression.\n"+
			"  Method mocks in rewire are global (all instances of the type share one mock), "+
			"so binding to a specific instance is not supported.\n"+
			"  Change:\n"+
			"      rewire.Func(t, instance.Method, replacement)\n"+
			"  to the method expression form, e.g.:\n"+
			"      rewire.Func(t, (*pkg.Type).Method, replacement)\n"+
			"  The resolved name was %q; the underlying method is %q.",
		name, name, cleaned,
	)
}

func funcName[F any](f F) string {
	v := reflect.ValueOf(f)
	if v.Kind() != reflect.Func {
		panic("rewire: argument must be a function")
	}
	pc := v.Pointer()
	rf := runtime.FuncForPC(pc)
	if rf == nil {
		panic("rewire: cannot resolve function name")
	}
	return rf.Name()
}
