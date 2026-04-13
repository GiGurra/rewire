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

var (
	registry     sync.Map // func name (string) → mock var pointer (any)
	realRegistry sync.Map // func name (string) → real function value (any)
)

// Register maps a fully-qualified function name to a pointer to its mock variable.
// This is called by generated init() code — users should not call it directly.
func Register(funcName string, mockVarPtr any) {
	registry.Store(funcName, mockVarPtr)
}

// RegisterReal maps a fully-qualified function name to its pre-rewrite
// implementation. Called by generated init() code — users should not call
// it directly. Used by Real to return the real function for spy-style tests.
//
// For non-generic targets there is one real per name. For generic targets
// there is one real per instantiation (per type-argument combination), and
// the codegen calls this once per instantiation. The registry key is
// composed of the function name plus the function's runtime type
// signature so that generic instantiations don't collide.
func RegisterReal(funcName string, realFn any) {
	typeKey := reflect.TypeOf(realFn).String()
	realRegistry.Store(funcName+"|"+typeKey, realFn)
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

	// For generic functions the registry entry is a *sync.Map keyed on the
	// type signature of the specific instantiation; each instantiation is
	// mocked independently.
	if genericMockMap, ok := resolveGenericMockMap(t, original); ok {
		key := reflect.TypeOf(original).String()
		genericMockMap.Store(key, replacement)
		t.Cleanup(func() { genericMockMap.Delete(key) })
		return
	}

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

// Real returns the pre-rewrite implementation of original — useful for
// spy-style tests that want to delegate to the real function from inside
// a mock closure.
//
// Typical usage: capture the real implementation before installing the
// mock, then call it from within the replacement:
//
//	realGetwd := rewire.Real(t, os.Getwd)
//	rewire.Func(t, os.Getwd, func() (string, error) {
//	    path, err := realGetwd()
//	    if err != nil {
//	        return "", err
//	    }
//	    return path + "/wrapped", nil
//	})
//
// Real requires the function to be targeted by a rewire.Func call
// somewhere in the module, so that the compiler wrapper (and thus the
// real alias) is actually emitted.
func Real[F any](t *testing.T, original F) F {
	t.Helper()

	if msg := validateFuncArgument(original); msg != "" {
		t.Fatal(msg)
		var zero F
		return zero
	}

	name := funcName(original)

	if msg := methodValueError(name); msg != "" {
		t.Fatal(msg)
		var zero F
		return zero
	}

	// Look up via composite key (name + runtime type signature) so
	// generic instantiations and non-generic functions resolve through
	// the same path. The codegen emits one RegisterReal per unique
	// type signature at compile time.
	typeKey := reflect.TypeOf(original).String()
	realFn, ok := realRegistry.Load(name + "|" + typeKey)
	if !ok {
		t.Fatalf("rewire: no real implementation registered for %s.\n"+
			"  rewire.Real requires the function to be targeted by a rewire.Func call\n"+
			"  somewhere in the test module so the compiler wrapper is emitted.\n"+
			"  If nothing else mocks it, you can simply call the function directly.",
			name)
		var zero F
		return zero
	}

	typed, ok := realFn.(F)
	if !ok {
		t.Fatalf("rewire: internal error: registered real implementation for %s has type %T, expected %T",
			name, realFn, *new(F))
		var zero F
		return zero
	}
	return typed
}

// resolveGenericMockMap checks whether original is a generic-function
// target and, if so, returns the per-instantiation mock sync.Map. Returns
// (nil, false) for non-generic targets. Validation of the argument should
// happen in the caller before this is invoked.
func resolveGenericMockMap[F any](t *testing.T, original F) (*sync.Map, bool) {
	t.Helper()

	if msg := validateFuncArgument(original); msg != "" {
		t.Fatal(msg)
		return nil, false
	}

	name := funcName(original)

	if msg := methodValueError(name); msg != "" {
		t.Fatal(msg)
		return nil, false
	}

	entry, ok := registry.Load(name)
	if !ok {
		return nil, false
	}
	// Non-generic entries are *func(...). Generic entries are *sync.Map.
	if m, ok := entry.(*sync.Map); ok {
		return m, true
	}
	return nil, false
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

	// Generic path: delete the instantiation-specific mock entry.
	if genericMockMap, ok := resolveGenericMockMap(t, original); ok {
		genericMockMap.Delete(reflect.TypeOf(original).String())
		return
	}

	elemVal := resolveMockVar(t, original)
	elemVal.Set(reflect.Zero(elemVal.Type()))
}

// resolveMockVar returns a reflect.Value addressing the package-level mock
// variable for original, or fatally fails the test with a targeted
// diagnostic if the function cannot be mocked.
func resolveMockVar[F any](t *testing.T, original F) reflect.Value {
	t.Helper()

	if msg := validateFuncArgument(original); msg != "" {
		t.Fatal(msg)
		return reflect.Value{}
	}

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

// validateFuncArgument checks that f is a usable function value and returns
// a diagnostic message if not, or "" if the argument is valid. It is
// intentionally decoupled from *testing.T so it can be unit-tested directly.
func validateFuncArgument[F any](f F) string {
	v := reflect.ValueOf(f)
	if !v.IsValid() {
		return "rewire: function argument is invalid (untyped nil).\n" +
			"  Pass a function reference such as bar.Greet or os.Getwd."
	}
	if v.Kind() != reflect.Func {
		return fmt.Sprintf(
			"rewire: expected a function, got value of type %s (kind %s).\n"+
				"  rewire.Func / rewire.Restore take a function reference like bar.Greet\n"+
				"  or os.Getwd — not a variable, literal, struct, or field.",
			v.Type(), v.Kind(),
		)
	}
	if v.IsNil() {
		return "rewire: function argument is nil.\n" +
			"  Pass a real function reference like bar.Greet, not a nil function variable."
	}
	return ""
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

// funcName assumes f has already been validated as a non-nil function value
// (see validateFuncArgument). Callers outside resolveMockVar must validate
// first, or FuncForPC may return nil and this will panic.
//
// For generic instantiations, FuncForPC returns a name with a trailing
// "[...]" placeholder (e.g. "pkg.Map[...]" for every instantiation of
// Map, regardless of the actual type arguments). We strip it so that
// registry lookups can use the same key as the codegen-emitted
// registration calls, which only know the base function name.
func funcName[F any](f F) string {
	name := runtime.FuncForPC(reflect.ValueOf(f).Pointer()).Name()
	return strings.TrimSuffix(name, "[...]")
}
