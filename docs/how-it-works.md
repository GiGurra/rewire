# How It Works

Rewire uses Go's `-toolexec` flag to intercept the compiler during `go test`. It rewrites targeted functions in-memory — source files on disk are never modified.

## The pipeline

1. **Pre-scan** — On the first compiler invocation, rewire walks your module's `_test.go` files and parses them with `go/ast`. It finds all `rewire.Func` calls and builds a target list (e.g., `bar.Greet`, `math.Pow`, `(*Server).Handle`).

2. **Targeted rewrite** — When the compiler processes a package containing targeted functions, rewire:
    - Reads the source files from the compiler's argument list
    - Rewrites only the specific functions in the target list
    - Writes rewritten source to temp files
    - Passes the temp file paths to the real compiler

3. **Registration** — When compiling a test package, rewire generates an `init()` function that registers mock variable pointers in a runtime registry. This connects the test binary to the mock variables.

4. **Runtime swap** — At test time, `rewire.Func(t, bar.Greet, replacement)`:
    - Calls `runtime.FuncForPC(reflect.ValueOf(bar.Greet).Pointer())` to get the function's fully-qualified name
    - Looks up the mock variable pointer in the registry
    - Sets the mock variable to the replacement via `reflect`
    - Registers a `t.Cleanup` to restore the original

## The rewrite transformation

Only targeted functions are rewritten. Everything else passes through untouched.

Given:

```go
func Greet(name string) string {
    return "Hello, " + name + "!"
}
```

Rewire produces (in-memory, during compilation only):

```go
var Mock_Greet func(name string) string

var Real_Greet = _real_Greet

func Greet(name string) string {
    if _rewire_mock := Mock_Greet; _rewire_mock != nil {
        return _rewire_mock(name)
    }
    return _real_Greet(name)
}

func _real_Greet(name string) string {
    return "Hello, " + name + "!"
}
```

The wrapper checks `Mock_Greet` on every call. When nil (the default), the original implementation runs — the overhead is a single nil check.

`Real_Greet` is an exported alias holding the pre-rewrite implementation. `rewire.Real(t, bar.Greet)` looks it up via a second registry so spy-style tests can delegate to the original from inside a mock closure.

## Method rewriting

Methods follow the same pattern but include the receiver:

```go
// Original
func (s *Server) Handle(req string) string {
    return "handled " + req
}

// Rewritten (in-memory)
var Mock_Server_Handle func(*Server, string) string

var Real_Server_Handle = (*Server)._real_Server_Handle

func (s *Server) Handle(req string) string {
    if _rewire_mock := Mock_Server_Handle; _rewire_mock != nil {
        return _rewire_mock(s, req)
    }
    return s._real_Server_Handle(req)
}

func (s *Server) _real_Server_Handle(req string) string {
    return "handled " + req
}
```

The `Real_Server_Handle` alias is a method expression of type `func(*Server, string) string`, so a spy can call it as `real(server, req)`.

## Per-instance method dispatch

When the scanner sees at least one `rewire.InstanceFunc(t, instance, target, ...)` or `rewire.RestoreInstanceFunc(...)` call referencing a pointer-receiver method, the rewriter emits an additional dispatch path on top of the shape above. A per-method `sync.Map` is added, keyed on the receiver pointer, and the wrapper body consults it before the global mock:

```go
// Rewritten (in-memory) when rewire.InstanceFunc targets (*Server).Handle
var Mock_Server_Handle            func(*Server, string) string
var Mock_Server_Handle_ByInstance sync.Map  // new — only emitted if InstanceFunc is used

func (s *Server) Handle(req string) string {
    // 1. Per-instance lookup — keyed on the receiver pointer.
    if raw, ok := Mock_Server_Handle_ByInstance.Load(s); ok {
        if fn, ok := raw.(func(*Server, string) string); ok {
            return fn(s, req)
        }
    }
    // 2. Global mock fallthrough — existing behavior.
    if _rewire_mock := Mock_Server_Handle; _rewire_mock != nil {
        return _rewire_mock(s, req)
    }
    // 3. Real implementation.
    return s._real_Server_Handle(req)
}
```

Dispatch order is **per-instance → global → real**, so `rewire.InstanceFunc` overrides `rewire.Func` for the specific receiver while other instances still see the global mock (or the real body).

**Emission is opt-in.** Tests that only use `rewire.Func` on methods don't get the `_ByInstance` sync.Map or the extra lookup — the scanner gates emission on whether any test in the module references the method via `InstanceFunc` / `RestoreInstanceFunc`. Zero per-call overhead for tests that don't need per-instance scoping.

**Three restore verbs.** `rewire.RestoreFunc(t, target)` clears the global mock for a function or method. `rewire.RestoreInstance(t, instance)` walks every registered `_ByInstance` sync.Map and drops entries keyed on that instance — all per-instance mocks bound to the receiver in one call. `rewire.RestoreInstanceFunc(t, instance, target)` clears exactly one `(instance, method)` pair.

**`any(instance)` as the key.** `rewire.InstanceFunc` stores the receiver as `any(instance)` so interface equality compares both dynamic type and pointer value. This matters for generic methods: `*Container[int]` and `*Container[string]` keys never collide even at the same address.

## Interface mocks via `rewire.NewMock[T]`

For interface mocks, there's no production body to rewrite — the interface has no implementation. Instead, rewire synthesizes a concrete backing struct into the test package at compile time.

1. **Scanner** — walks `_test.go` files and collects every `rewire.NewMock[X]` reference. Each one names an interface type that needs a backing struct.
2. **Interface resolution** — for each mocked interface, locate its declaring package via `go/build`, parse its source, extract the method set.
3. **Struct synthesis** — emit a concrete struct type that satisfies the interface. Each method body consults a per-method `_ByInstance sync.Map` (the same mechanism as per-instance method mocks) and falls back to zero-value returns when nothing is stubbed.
4. **Factory + registry** — the synthesized file includes an `init()` that registers (a) a factory function mapping the interface's fully-qualified name to a constructor, and (b) every per-method `_ByInstance` table with `rewire.RegisterByInstance`.
5. **Injection** — the generated file is written to a temp path and appended to the compiler's argument list. It's a real `*_test.go` file from the compiler's perspective, but it only exists for the duration of the compile.

Concretely, `rewire.NewMock[bar.GreeterIface](t)` produces code like:

```go
// Synthesized at compile time, never written to disk:
type _rewire_mock_bar_GreeterIface struct{ _ [1]byte }

var Mock__rewire_mock_bar_GreeterIface_Greet_ByInstance sync.Map

func (m *_rewire_mock_bar_GreeterIface) Greet(name string) (_r0 string) {
    if raw, ok := Mock__rewire_mock_bar_GreeterIface_Greet_ByInstance.Load(m); ok {
        if fn, ok := raw.(func(bar.GreeterIface, string) string); ok {
            return fn(m, name)
        }
    }
    return // zero value
}

func init() {
    rewire.RegisterMockFactory("github.com/example/bar.GreeterIface", func() any {
        return &_rewire_mock_bar_GreeterIface{}
    })
    rewire.RegisterByInstance(
        "github.com/example/bar.GreeterIface.Greet",
        &Mock__rewire_mock_bar_GreeterIface_Greet_ByInstance,
    )
}
```

At test time:

- `rewire.NewMock[bar.GreeterIface](t)` looks up the factory by the interface's full name and returns `factory()` type-asserted back to `bar.GreeterIface`.
- `rewire.InstanceFunc(t, mock, bar.GreeterIface.Greet, replacement)` resolves `bar.GreeterIface.Greet` via `runtime.FuncForPC` — which does return a stable, parseable name for interface method expressions — and stores the replacement in the registered `_ByInstance` sync.Map.
- The backing struct's method body loads from the same sync.Map on every call, so stubs set via `InstanceFunc` route correctly and unstubbed methods return zero values.

**The `[1]byte` padding field is load-bearing.** Go's spec explicitly allows pointers to distinct zero-size variables to compare equal, which means two `&emptyStruct{}` allocations may share an address and collide in the per-instance sync.Map. A one-byte padding field forces distinct allocations to get distinct addresses.

**Receiver type bridging.** The stored replacement has signature `func(bar.GreeterIface, string) string` — the user passes a function of the interface method expression's type. When the generated method assembles its type assertion, it uses exactly that type, and calls `fn(m, name)` where `m` is the concrete backing struct pointer. Go's implicit assignability rule converts `m` to `bar.GreeterIface` at the call site.

## Build cache

Go's build cache keys on compilation inputs including the toolexec binary. The recommended setup uses a separate cache for tests:

```bash
GOFLAGS="-toolexec=rewire" GOCACHE="$HOME/.cache/rewire-test" go test ./...
```

This keeps `go build` (production) and `go test` (with rewire) from sharing cached artifacts.

## Generic functions

Generic functions take a separate rewrite path because Go doesn't allow generic package-level variables — you can't write `var Mock_Map[T, U any] func(...)`. Instead, the rewriter emits a single `sync.Map` per generic function and dispatches on the concrete instantiation's type signature:

```go
// Rewritten (in-memory)
var Mock_Map sync.Map  // key: type-sig string, value: mock fn (any)

func Real_Map[T, U any](in []T, f func(T) U) []U {
    return _real_Map(in, f)
}

func Map[T, U any](in []T, f func(T) U) []U {
    if raw, ok := Mock_Map.Load(reflect.TypeOf(Map[T, U]).String()); ok {
        if typed, ok := raw.(func([]T, func(T) U) []U); ok {
            return typed(in, f)
        }
    }
    return _real_Map(in, f)
}

func _real_Map[T, U any](in []T, f func(T) U) []U { /* original body */ }
```

The `reflect.TypeOf(Map[T, U]).String()` self-reference produces the concrete instantiation's signature (e.g. `func([]int, func(int) string) []string`), which exactly matches what `reflect.TypeOf(bar.Map[int, string]).String()` produces at the `rewire.Func` call site. Both sides compute the same lookup key with no coordination needed.

Because Go doesn't support runtime generic instantiation, `rewire.Real` for generics requires the concrete instantiations to be materialized at compile time. The toolexec pre-scan collects every type-argument combination referenced in `rewire.{Func,Real,Restore}` calls and the codegen emits one `rewire.RegisterReal("pkg.Map", pkg.Real_Map[int, string])` call per unique instantiation. At runtime `rewire.Real` looks up the registry by a composite `name + typeKey` key.

## Inlining

Go's compiler aggressively inlines small leaf functions. For rewire-rewritten code, we verified that the inliner inlines:

1. The wrapper function (the one that does the `Mock_` nil check) into its callers, and
2. `_real_<Name>` into the wrapper itself.

The result at every inlined call site is the full unrolled form:

```go
if _rewire_mock := Mock_X; _rewire_mock != nil {
    return _rewire_mock(args)
}
return <real body>   // inlined _real_X
```

So the mock check fires at every call site — inlining can't bypass it — and the fast path (no mock installed) costs only a nil check beyond the original implementation. `scripts/check-inlining.sh` runs in CI and asserts the expected inlining decisions appear in `go build -gcflags=-m=2` output.

## Compiler intrinsics

Some functions (e.g., `math.Abs`, `math.Sqrt`) are replaced by CPU instructions at the **call site** by the Go compiler. Even though rewire can rewrite the function body, callers bypass it entirely.

Rewire detects these by parsing the compiler's own intrinsics registry (`$GOROOT/src/cmd/compile/internal/ssagen/intrinsics.go`). If you try to mock an intrinsic, the build fails with a clear error:

```
rewire: error: function math.Abs cannot be mocked.
  It is a compiler intrinsic — the Go compiler replaces calls to it
  with a CPU instruction, bypassing any mock wrapper.
```

## Scan caching

The test file scan happens once per build session (keyed on parent PID). A file lock ensures only one toolexec process scans; others wait for the cached result. This avoids redundant work when `go test` invokes many parallel compiler processes.
