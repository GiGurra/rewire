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
	registry            sync.Map // func name (string) → mock var pointer (any)
	realRegistry        sync.Map // func name (string) → real function value (any)
	byInstanceRegistry  sync.Map // func name (string) → *sync.Map (per-method per-instance table)
	mockFactoryRegistry sync.Map // interface name (string) → func() any (mock instance factory)
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

// RegisterByInstance maps a fully-qualified method name to a pointer to
// its per-instance mock table (a *sync.Map). Called by generated init()
// code for methods that are referenced by at least one
// rewire.InstanceMethod / rewire.RestoreInstanceMethod call in the test
// module. Users should not call it directly.
func RegisterByInstance(funcName string, byInstanceMap *sync.Map) {
	byInstanceRegistry.Store(funcName, byInstanceMap)
}

// RegisterMockFactory registers a factory that produces a fresh mock
// instance satisfying the interface type I. Called by generated init()
// code emitted during toolexec compilation for each interface
// referenced via rewire.NewMock[I]. Users should not call it directly.
//
// The registry key is derived from I via reflect.TypeOf — the same
// computation NewMock[I] uses at lookup time. Passing the type as a
// type parameter (rather than a pre-formatted string) means the
// generated file doesn't have to format the key, doesn't have to
// import reflect, and can never drift out of sync with reflect's
// canonical name format for generic interface instantiations.
//
// The factory returns any-typed so the generated file doesn't have
// generics in scope; NewMock does the type assertion back to I at the
// call site.
func RegisterMockFactory[I any](factory func() any) {
	typ := reflect.TypeFor[I]()
	if typ == nil || typ.Kind() != reflect.Interface || typ.PkgPath() == "" || typ.Name() == "" {
		// The toolexec codegen only emits RegisterMockFactory for
		// named, exported interfaces — anything else is a bug in
		// rewire itself, not user error. Fail loudly so we catch it.
		panic(fmt.Sprintf("rewire.RegisterMockFactory: invalid interface type %v (must be a named, exported interface)", typ))
	}
	key := typ.PkgPath() + "." + typ.Name()
	mockFactoryRegistry.Store(key, factory)
}

// NewMock returns a fresh mock instance of interface type I. The mock's
// backing struct is synthesized at compile time by the rewire toolexec
// wrapper — no go:generate, no committed mock files.
//
// Usage:
//
//	greeter := rewire.NewMock[bar.GreeterIface](t)
//	rewire.InstanceMethod(t, greeter, bar.GreeterIface.Greet, func(g bar.GreeterIface, name string) string {
//	    return "mocked: " + name
//	})
//	svc := NewService(greeter)
//
// The returned value satisfies I and can be passed wherever I is
// expected. Stub methods via rewire.InstanceMethod using interface
// method expressions (bar.GreeterIface.Greet) as the target. Unstubbed
// methods return zero values.
//
// Requires -toolexec=rewire. Each test that references NewMock[X] in a
// test file causes the compiler wrapper to emit a backing struct and
// factory registration for X. If X isn't actually referenced by
// NewMock in any test, the factory isn't registered and NewMock fails
// with a targeted error.
//
// Works only for exported interface types. I must be resolvable by
// reflect.TypeFor and have a non-empty PkgPath.
func NewMock[I any](t *testing.T) I {
	t.Helper()

	var zero I
	typ := reflect.TypeFor[I]()
	if typ == nil {
		t.Fatal("rewire.NewMock: type parameter I has no runtime type")
		return zero
	}
	if typ.Kind() != reflect.Interface {
		t.Fatalf("rewire.NewMock: type parameter I must be an interface, got %s (kind %s)", typ, typ.Kind())
		return zero
	}
	if typ.PkgPath() == "" || typ.Name() == "" {
		t.Fatalf("rewire.NewMock: interface %s has no package path — only named, exported interface types are supported", typ)
		return zero
	}
	key := typ.PkgPath() + "." + typ.Name()

	entry, ok := mockFactoryRegistry.Load(key)
	if !ok {
		t.Fatalf("rewire.NewMock: no mock factory registered for %s.\n"+
			"  NewMock requires the interface to be referenced by a rewire.NewMock[%s] call\n"+
			"  in a test file so the compiler wrapper can emit a backing struct.\n"+
			"  Also make sure -toolexec=rewire is active.",
			key, typ.Name())
		return zero
	}
	factory, ok := entry.(func() any)
	if !ok {
		t.Fatalf("rewire: internal error: mock factory for %s has type %T, expected func() any", key, entry)
		return zero
	}
	result, ok := factory().(I)
	if !ok {
		t.Fatalf("rewire: internal error: mock factory for %s returned a value that does not satisfy %s", key, typ)
		return zero
	}
	return result
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

// Restore clears active mocks. It has two modes depending on what you
// pass as the second argument:
//
//  1. Function target (e.g. bar.Greet, (*bar.Server).Handle) — clears
//     the global mock, so subsequent calls use the real implementation.
//     This is the original behavior.
//
//  2. Instance value (a pointer receiver) — clears every per-instance
//     mock currently scoped to that instance. Useful when a test set up
//     multiple per-instance mocks via rewire.InstanceMethod and wants to
//     drop them all mid-test without listing each method.
//
// Restore is optional — the test's automatic cleanup already restores
// mocks at test end — but it lets you end a mock mid-test. It is
// idempotent and safe to call any number of times.
func Restore[T any](t *testing.T, target T) {
	t.Helper()

	// Branch on the runtime kind: functions → global-mock restore,
	// anything else → treat as an instance and walk the byInstance
	// registry.
	v := reflect.ValueOf(target)
	if !v.IsValid() || v.Kind() != reflect.Func {
		restoreInstanceAll(target)
		return
	}

	// Generic path: delete the instantiation-specific mock entry.
	if genericMockMap, ok := resolveGenericMockMap(t, target); ok {
		genericMockMap.Delete(reflect.TypeOf(target).String())
		return
	}

	elemVal := resolveMockVar(t, target)
	elemVal.Set(reflect.Zero(elemVal.Type()))
}

// InstanceMethod installs a per-instance mock for a pointer-receiver
// method. Unlike rewire.Func, which replaces the method for every
// instance of the type, InstanceMethod scopes the replacement to one
// specific receiver. Calls from other instances still go through the
// global mock (if any) or the real implementation.
//
// Dispatch order inside the method wrapper:
//  1. Per-instance mock matching the receiver, if one is set.
//  2. Global rewire.Func mock, if one is set.
//  3. The real implementation.
//
// Requires -toolexec=rewire. The compiler wrapper for the target must
// have been emitted with per-instance support, which happens whenever
// any test in the module references the target via InstanceMethod or
// RestoreInstanceMethod.
//
// Usage:
//
//	s1 := &bar.Server{Name: "primary"}
//	s2 := &bar.Server{Name: "secondary"}
//
//	rewire.InstanceMethod(t, s1, (*bar.Server).Handle, func(s *bar.Server, req string) string {
//	    return "primary mock: " + req
//	})
//	// s2 still runs the real Handle.
//
// Restrictions:
//   - target must be a pointer-receiver method expression (*Type).Method.
//     Value receivers and free functions are not supported — the test
//     fails immediately via t.Fatal.
//   - instance must be a non-nil pointer value.
//
// Automatic cleanup via t.Cleanup removes the per-instance entry at
// test end.
func InstanceMethod[I any, F any](t *testing.T, instance I, original F, replacement F) {
	t.Helper()

	if msg := validateFuncArgument(original); msg != "" {
		t.Fatal(msg)
		return
	}
	if reflect.ValueOf(instance).Kind() != reflect.Pointer || reflect.ValueOf(instance).IsNil() {
		t.Fatal("rewire.InstanceMethod: instance must be a non-nil pointer value")
		return
	}

	m := resolveByInstanceMap(t, original)
	if m == nil {
		return
	}

	// Store as any(instance). Reads from the wrapper use
	// sync.Map.Load(recv), which box the receiver into an interface
	// with the same (type, value) and compare by interface equality.
	m.Store(any(instance), replacement)
	t.Cleanup(func() { m.Delete(any(instance)) })
}

// RestoreInstanceMethod clears a single per-instance mock entry set by
// InstanceMethod. Useful for dropping one method's mock mid-test while
// leaving other per-instance mocks in place. Idempotent.
func RestoreInstanceMethod[I any, F any](t *testing.T, instance I, original F) {
	t.Helper()

	if msg := validateFuncArgument(original); msg != "" {
		t.Fatal(msg)
		return
	}

	m := resolveByInstanceMap(t, original)
	if m == nil {
		return
	}
	m.Delete(any(instance))
}

// restoreInstanceAll clears every per-instance mock scoped to instance
// across every registered per-method table. Called by Restore when the
// target is not a function value.
func restoreInstanceAll(instance any) {
	key := any(instance)
	byInstanceRegistry.Range(func(_, v any) bool {
		m, ok := v.(*sync.Map)
		if ok {
			m.Delete(key)
		}
		return true
	})
}

// resolveByInstanceMap returns the per-instance sync.Map for original,
// or fatally fails the test if no such table exists (target wasn't
// referenced by any InstanceMethod / RestoreInstanceMethod call, so the
// rewriter didn't emit the per-instance dispatch).
func resolveByInstanceMap[F any](t *testing.T, original F) *sync.Map {
	t.Helper()

	name := funcName(original)
	if msg := methodValueError(name); msg != "" {
		t.Fatal(msg)
		return nil
	}

	entry, ok := byInstanceRegistry.Load(name)
	if !ok {
		t.Fatalf("rewire.InstanceMethod: no per-instance dispatch table registered for %s.\n"+
			"  InstanceMethod requires the target to be a pointer-receiver method referenced by\n"+
			"  rewire.InstanceMethod or rewire.RestoreInstanceMethod somewhere in the test module,\n"+
			"  so that the compiler wrapper is emitted with per-instance support.",
			name)
		return nil
	}
	m, ok := entry.(*sync.Map)
	if !ok {
		t.Fatalf("rewire: internal error: per-instance entry for %s is not a *sync.Map", name)
		return nil
	}
	return m
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
// For generic instantiations, FuncForPC inserts a literal "[...]"
// placeholder wherever a type argument list would live — as a suffix
// for plain generic functions ("pkg.Map[...]") and inside the receiver
// for generic methods ("pkg.(*Container[...]).Add"). We strip every
// occurrence so that registry lookups match the canonical name the
// codegen emits, which never contains type arguments.
func funcName[F any](f F) string {
	name := runtime.FuncForPC(reflect.ValueOf(f).Pointer()).Name()
	return strings.ReplaceAll(name, "[...]", "")
}
