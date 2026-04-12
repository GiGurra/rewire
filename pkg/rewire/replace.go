package rewire

import (
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

	name := funcName(original)

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
	}

	ptrVal := reflect.ValueOf(mockPtrAny)
	elemVal := ptrVal.Elem()

	// Save current value (may be nil)
	oldVal := reflect.New(elemVal.Type()).Elem()
	oldVal.Set(elemVal)

	// Set replacement
	elemVal.Set(reflect.ValueOf(replacement))

	t.Cleanup(func() {
		elemVal.Set(oldVal)
	})
}

// Replace swaps the value at target with replacement for the duration of the test.
// This is the low-level API — prefer Func for a cleaner experience.
func Replace[F any](t *testing.T, target *F, replacement F) {
	t.Helper()
	old := *target
	*target = replacement
	t.Cleanup(func() { *target = old })
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
