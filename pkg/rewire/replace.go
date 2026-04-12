package rewire

import (
	"reflect"
	"runtime"
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
		t.Fatalf("rewire.Func: %s is not registered — is -toolexec=rewire active?", name)
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
