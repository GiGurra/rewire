# TODO — Toolexec-driven interface mocks (no `go:generate`)

**Status:** Phase 1 shipped — Phase 2 (embedded interfaces, same-package types) and Phase 3 (generic interfaces) pending.
**Layer:** scanner + toolexec codegen + `pkg/rewire` (new API) + reuse of `internal/mockgen`

## Motivation

Today, interface mocks require a `rewire mock` CLI step or a `go:generate` directive, and the generated `mock_*_test.go` files are committed to the repo. Every Go mocking library I've looked at (mockery, gomock, moq, testify/mock, counterfeiter) has the same requirement in some form.

Rewire already has the machinery to generate source at compile time and inject it into a package's compile args (the toolexec wrapper for `_rewire_register_test.go`). If we reuse that mechanism for interface mocks, we get something genuinely new: **mock structs that exist only during the compile, with zero repo footprint and zero `go generate` ceremony**. Users write `rewire.NewMock[bar.GreeterIface](t)` and it Just Works.

## Spike results (confirmed before planning)

A small Go program verified the keystone assumption: `runtime.FuncForPC` on an interface method expression returns a stable, canonical name.

```
Greeter.Greet          : name="main.Greeter.Greet"        type=func(main.Greeter, string) string
Greeter.Farewell       : name="main.Greeter.Farewell"     type=func(main.Greeter, string) string
(*Concrete).Greet      : name="main.(*Concrete).Greet"    type=func(*main.Concrete, string) string
```

This means:

- The scanner's existing `extractMockTarget` parser (which handles `pkg.Type.Method` for value receivers) already parses interface method expressions — no new syntax to invent.
- The runtime name matches exactly what `parseMethodTarget` expects.
- Two references to the same method expression share the same PC, so FuncForPC lookups are stable.
- **`rewire.InstanceMethod` works verbatim as the stubbing API.** No new `rewire.Stub` function needed.

## Target API

```go
func TestService_GreetingFlow(t *testing.T) {
    // Creates a fresh backing struct implementing bar.GreeterIface.
    // No go:generate, no committed mock file.
    greeter := rewire.NewMock[bar.GreeterIface](t)

    // Stubbing uses the per-instance method mock we already built.
    // The target is an interface method expression, fully type-checked.
    rewire.InstanceMethod(t, greeter, bar.GreeterIface.Greet, func(g bar.GreeterIface, name string) string {
        return "mocked: " + name
    })
    rewire.InstanceMethod(t, greeter, bar.GreeterIface.Farewell, func(g bar.GreeterIface, name string) string {
        return "bye mocked: " + name
    })

    // Pass the mock anywhere a bar.GreeterIface is expected.
    svc := NewService(greeter)
    got := svc.HelloFlow("Alice")
    // ...
}

// Restore a single method mid-test:
rewire.RestoreInstanceMethod(t, greeter, bar.GreeterIface.Greet)

// Or clear every mock scoped to this instance:
rewire.Restore(t, greeter)
```

Note what's NOT in the API:

- No `rewire.Stub` / `rewire.Mock` / other new verbs. `InstanceMethod` is the only stubbing entry point.
- No field access on the generated struct (which would break once we change the codegen or if the user's IDE can't see the generated type).
- No string-based method selection (the method expression `bar.GreeterIface.Greet` is compile-time type-safe).

## What toolexec has to do that it doesn't do today

### 1. Scanner: detect `rewire.NewMock[T]()` calls

Walk AST in `_test.go` files. For every call expression:

```go
rewire.NewMock[bar.GreeterIface](t)
```

…extract the type argument. In Go AST this is `IndexExpr{X: SelectorExpr{X: rewire, Sel: NewMock}, Index: SelectorExpr{X: bar, Sel: GreeterIface}}`. Generalize to `IndexListExpr` once generic interfaces are supported.

Add to a new `mockedInterfaces map[importPath][]typeName`.

### 2. Interface method set resolution

Given `("github.com/example/bar", "GreeterIface")`, locate the declaring package via `go/build.Default.Import(path, "", build.FindOnly)`, parse its non-test Go files, find `type GreeterIface interface { ... }`, and extract method signatures.

Reuse `internal/mockgen/mockgen.go` for this — it already parses interface declarations and handles imports, variadic parameters, multi-return, and unnamed parameters. Factor any shared logic out of `mockgen.go` into a helper that both the `rewire mock` CLI and the toolexec codegen can call.

### 3. Struct + method generation

Toolexec emits a file like `_rewire_mock_bar_GreeterIface_test.go` into the test package's compile args with:

```go
package foo

import (
    "github.com/example/bar"
    "github.com/GiGurra/rewire/pkg/rewire"
)

type _rewire_mock_bar_GreeterIface struct{}

var Mock__rewire_mock_bar_GreeterIface_Greet    func(*_rewire_mock_bar_GreeterIface, string) string
var Mock__rewire_mock_bar_GreeterIface_Greet_ByInstance sync.Map

func (m *_rewire_mock_bar_GreeterIface) Greet(name string) string {
    // same per-instance → global → real dispatch the rewriter emits,
    // except "real" here is a zero-value return since interfaces have no body.
    ...
}
// ... same pattern for Farewell and every other method on the interface.

func init() {
    // Register a factory so rewire.NewMock[bar.GreeterIface] knows how
    // to produce one of these instances when the user asks for it.
    rewire.RegisterMockFactory("github.com/example/bar.GreeterIface", func() any {
        return &_rewire_mock_bar_GreeterIface{}
    })
    // Register per-instance dispatch tables, same as generated init for
    // rewritten methods.
    rewire.RegisterByInstance(
        "github.com/example/bar.GreeterIface.Greet",
        &Mock__rewire_mock_bar_GreeterIface_Greet_ByInstance,
    )
    // ...
}
```

### 4. Bridging the receiver type mismatch

This is the subtle bit. The user's replacement has signature `func(bar.GreeterIface, string) string` (the method expression type). The generated struct's method has receiver `*_rewire_mock_bar_GreeterIface`. The per-instance dispatch stores the replacement and reads it from the generated method body — but the receiver types don't match, so a direct type assertion fails.

Options:

- **Option A**: Generated method wraps the stored replacement in a cast — check receiver-first, cast mock to `bar.GreeterIface`, then invoke replacement. Requires the generated code to know the interface name and import path. Cleanest.
- **Option B**: Store the replacement under a different type-erased key, invoke via `reflect.Value.Call`. Slower; loses some type safety at dispatch time.

Option A is fine and matches how the rewriter already handles receiver-first mock signatures.

### 5. `pkg/rewire.NewMock[T]`

```go
func NewMock[T any](t *testing.T) T {
    var zero T
    typ := reflect.TypeOf(&zero).Elem()
    key := typ.PkgPath() + "." + typ.Name()
    entry, ok := mockFactoryRegistry.Load(key)
    if !ok {
        t.Fatalf("rewire.NewMock: no mock registered for %s — did you reference it via rewire.NewMock[%s] in a test file?", key, typ.Name())
        return zero
    }
    return entry.(func() any)().(T)
}

func RegisterMockFactory(name string, factory func() any) {
    mockFactoryRegistry.Store(name, factory)
}
```

The factory is registered in the generated file's `init()`. Non-reflect, non-generic invocation — cheap, works at test boot.

## Phases

### Phase 1 — MVP, narrow scope (SHIPPED)

- Non-generic interface ✓
- No embedded interfaces ✓ (rejected with clear error)
- Methods using builtin or already-qualified types (e.g. `string`, `context.Context`, `*http.Request`) ✓
- Types from the interface's own declaring package → NOT YET (rejected implicitly — AST idents aren't qualified, will emit broken source)
- Multiple mocks per test, same or different interface ✓
- End-to-end tests covering the full loop ✓

One important footgun discovered during implementation:
**the backing struct must have non-zero size**. An empty struct lets Go coalesce
multiple `&mock{}` allocations to the same address (Go spec permits this for
zero-size types), which breaks per-instance dispatch since the sync.Map keys
on the receiver pointer. Fixed by emitting `struct{ _ [1]byte }`.

Also discovered: `go clean -cache` does not clean `$HOME/.cache/rewire-test`
when tests run with `GOCACHE=$HOME/.cache/rewire-test`. The toolexec test
workflow has to `rm -rf` that path explicitly when iterating on codegen
changes. Noted in CI and the local iteration loop.

### Phase 2 — robustness

- Embedded interfaces (`io.ReadCloser` embeds `Reader` + `Closer`)
- Cross-package types in method signatures (`func() (*other.Thing, error)`)
- Multiple mocks of different interfaces in one test
- Multiple mocks of the same interface in one test
- Variadic methods, multi-return, unnamed parameters (already supported by `internal/mockgen`)

### Phase 3 — generic interfaces

- `Store[T any]` with per-instantiation dispatch — same pattern as the existing generic method rewriting.

## Open questions

1. **Unexported interfaces.** If an interface is unexported in its declaring package, the test package can't reference `bar.greeterIface`. Should we try to support it somehow, or is "mocked interfaces must be exported" a reasonable limitation? (Probably reasonable; flag clearly.)
2. **Embedded interfaces from other packages.** `io.ReadCloser` embeds `io.Reader` and `io.Closer`. Resolving the full method set requires walking the embedded hierarchy across packages. Non-trivial but `go/types` can do it. For Phase 1 we can reject embedded interfaces with a clear error.
3. **IDE support for the generated struct.** Gopls can't see types that only exist at compile time. Accepted cost — the user never names the generated struct, only the interface. As long as the API design holds, this is invisible in practice.
4. **Diagnostics when the mock isn't wired up.** If someone writes `rewire.NewMock[foo.X]()` but `foo.X` isn't an interface, or the compile-time codegen fails to parse it, we need clear errors from `go test` not mystery "no mock registered" messages at runtime. Error paths at scanner time are the place to surface this.

## Effort estimate

- Scanner: ~60 lines — detect `NewMock[T]` via IndexExpr + IndexListExpr.
- Interface resolver: ~80 lines — refactor shared helper from `internal/mockgen`, plus new "resolve from import path" entry point.
- Codegen: ~150 lines — emit the struct, methods, init. Most of the heavy lifting is template text, already proven by `mockgen.go`.
- `pkg/rewire`: ~40 lines — `NewMock`, `RegisterMockFactory`, `mockFactoryRegistry`.
- Toolexec glue: ~30 lines — wire the new codegen output into the compile args.
- Tests: ~100 lines scanner unit + ~60 lines mockgen unit + ~150 lines end-to-end.
- Docs: new section in `docs/interface-mocks.md`, brief note in `README.md`.

Roughly **~600 lines** including tests — comparable to the per-instance feature we just shipped.

## Non-goals

- Replacing the existing `rewire mock` CLI. The committed-file workflow is still useful for users who want IDE visibility into the generated struct or who want to inspect what's produced. Both modes should coexist.
- Perfect parity with mockery's feature set. Start with the common case; add what users actually ask for.

## Exit criteria

- `rewire.NewMock[T](t)` returns a usable mock of any non-generic interface in the user's module, with zero `go generate` and zero committed mock files.
- Stubbing via `rewire.InstanceMethod(t, mock, I.Method, replacement)` works.
- `rewire.Restore(t, mock)` and `rewire.RestoreInstanceMethod(t, mock, I.Method)` work.
- Clear errors when the type argument isn't an interface or can't be resolved.
- Existing `rewire mock` CLI workflow continues to work unchanged.
- Inlining check still passes.
