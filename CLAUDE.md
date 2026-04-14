# CLAUDE.md

## Project overview

Rewire is a Go test mocking tool with two modes that share one toolexec pipeline:

1. **Function/method mocking (`rewire.Func` + toolexec)**: Uses `-toolexec` to intercept compilation and rewrite functions at compile time. Scans `_test.go` files for `rewire.Func` calls, builds a targeted list, and rewrites only those during compilation. Production source is never modified.
2. **Interface mock synthesis (`rewire.NewMock[T]` + toolexec)**: Same toolexec pipeline. Scans test files for `rewire.NewMock[I]` references, locates I's source, and synthesizes a backing struct at compile time. No `go:generate`, no committed files. Handles non-generic AND generic interfaces (arbitrary type-arg shapes including external-package types and nested generics), embedded interfaces (same-file, same-package, cross-package — including generic embeds where the outer's type parameter flows into the embed, walked via an `InterfaceResolver` callback the toolexec wrapper injects), methods referencing bare same-package types (auto-qualified with the declaring package alias by `qualifyBareTypes`, which runs BEFORE substitution with the interface's type parameter names as a skip set), and interfaces declared in files with dot imports (`import . "pkg"` — the walker detects the dot import, asks a `PackageTypeLister` callback for the dot-imported package's exported types, and gives those names priority over same-package qualification). Package resolution goes through `go list -find`, so `replace` directives in `go.mod`, workspace files (`go.work`), and vendor directories all work; stdlib packages take a `go/build.Default.Import` fast path.

Inspired by Erlang's meck.

## Build and test

```bash
# Build
go build ./...

# Install the binary (needed before toolexec can work)
go install ./cmd/rewire/

# Run all tests with toolexec (separate cache avoids conflicts with go build)
GOFLAGS="-toolexec=rewire" GOCACHE="$HOME/.cache/rewire-test" go test ./...

# Run just the rewriter unit tests (no toolexec needed)
go test ./internal/rewriter/

# Run just the mockgen unit tests (no toolexec needed)
go test ./internal/mockgen/

# Run toolexec integration tests (includes intrinsic detection test)
go test ./internal/toolexec/ -count=1

# Regenerate interface mocks (after changing interfaces)
go generate ./...
```

After changing rewire source: always `go install ./cmd/rewire/` before running tests with toolexec.

## Project structure

- `cmd/rewire/main.go` — Entry point. Detects toolexec mode (first arg is absolute path to a Go tool) vs CLI subcommand mode. Uses boa (github.com/GiGurra/boa) for CLI. The only CLI subcommand is `rewrite`, a debug helper that prints what the rewriter would do to a single file. Interface mock generation is purely toolexec-driven via `rewire.NewMock[T]` references in test files — there is no separate CLI invocation for it.
- `pkg/rewire/replace.go` — The public test API:
  - `Func[F any](t, original, replacement)` — replace a function or method (per-instantiation for generics).
  - `NewMock[I any](t)` — return a fresh mock instance of interface I. Triggers compile-time backing-struct synthesis for I via toolexec.
  - `InstanceMethod[I, F any](t, instance, original, replacement)` — per-receiver method mock. Used to stub interface methods on `NewMock` instances and to scope concrete-method mocks to one receiver.
  - `Real[F any](t, original)` — return the pre-rewrite implementation for spy-style tests.
  - `Restore[T any](t, target)` — overloaded: function target → clear global mock; instance target → clear all per-instance mocks on that instance.
  - `RestoreInstanceMethod[I, F any](t, instance, original)` — clear one per-instance entry.
  - `RegisterMockFactory[I](factory)`, `RegisterByInstance[F](funcName, &table, witness)`, `Register(funcName, mockVarPtr)`, `RegisterReal(funcName, fn)` — called by generated init() code, not by users. The two `[I]` / `[F]` forms use type parameters so reflect.TypeFor derives the registry key on both sides, avoiding any compile-time string formatting drift.
  - `Replace[F any](t, &target, replacement)` — low-level API, directly swaps a mock var by pointer.
- `internal/rewriter/rewriter.go` — AST rewriter:
  - `RewriteSource` — rewrites a single named function or method. Accepts `"Func"`, `"(*Type).Method"`, or `"Type.Method"` syntax. Skips bodyless functions.
  - `RewriteSourceOpts` — same plus a `ByInstance` option for emitting per-instance dispatch tables.
  - Special-cases generic functions and methods on generic types via `rewriteGenericFunction` / `rewriteGenericMethod`.
- `internal/mockgen/rewiregen.go` — Toolexec interface mock generator (`GenerateRewireMock`). Parses an interface's source, synthesizes a concrete backing struct, generates per-method dispatch tables, and emits a registration `init()`. Performs AST-level type-parameter substitution for generic interfaces and consults a per-instantiation type-arg import map to emit correct imports for type args from packages the interface's declaring file doesn't import.
- `internal/mockgen/helpers.go` — Shared AST-printing and parameter-handling helpers used by `rewiregen.go` (`ensureParamNames`, `fieldListToString`, `resultsToString`, `addResultNames`, `buildCallArgs`, `paramNames`, `isVariadicFunc`, `nodeToString`, `collectPkgRefs`).
- `internal/toolexec/toolexec.go` — Toolexec wrapper:
  - Intercepts `compile` invocations for any package with targeted functions.
  - Rewrites only the specific functions found in `rewire.Func` / `rewire.InstanceMethod` calls.
  - For test compilations: generates `_rewire_init_test.go` that registers mock var pointers AND runs `generateInterfaceMocks` to synthesize backing structs for any `rewire.NewMock[I]` references in the test sources.
- `internal/toolexec/scan.go` — Pre-scans `_test.go` files for `rewire.{Func,Real,Restore,InstanceMethod,RestoreInstanceMethod,NewMock}` calls. Builds (a) a map of import path → function/method names, (b) per-instantiation generic type-arg combos, (c) the byInstance subset, and (d) a `mockedInterfaces` map of `importPath → ifaceName → []mockInstance{TypeArgs, TypeArgImports}`. Handles `pkg.Func`, `pkg.Type.Method`, `(*pkg.Type).Method`, `pkg.Func[T]`, and `rewire.NewMock[pkg.Iface[T, U]]` patterns. Results cached per build (keyed on parent PID).
- `internal/toolexec/intrinsics.go` — Detects compiler intrinsic functions by parsing `$GOROOT/src/cmd/compile/internal/ssagen/intrinsics.go`. Intrinsics are replaced by CPU instructions at the call site, bypassing any wrapper.
- `example/` — End-to-end examples:
  - `bar/bar.go` — production functions and types (`Greet`, `Greeter`, generic `Container[T]`)
  - `bar/interfaces.go` — interfaces for mock generation (`GreeterIface`, `Store`, `Logger`, `HTTPClient`, `ContainerIface[T any]`, `CacheIface[K comparable, V any]`, `Repository[T any]`)
  - `foo/` — tests using `rewire.Func` (function/method/stdlib mocking) and `rewire.NewMock[T]` (interface mocks, generic interface mocks, dependency-injected services like `UserService` backed by `Repository[User]`)

## Key design decisions

- **Targeted rewriting**: Rewire pre-scans `_test.go` files for `rewire.Func` calls and only rewrites those specific functions. This solved the chicken-and-egg problem (dependencies compile before test packages) by doing a file walk + AST parse upfront.
- **toolexec over -overlay**: toolexec integrates with `go test` directly and handles per-package compilation naturally.
- **Separate test cache**: Recommended setup uses `GOCACHE=$HOME/.cache/rewire-test` for tests, keeping production build cache clean. If GOFLAGS is set globally instead, the overhead is negligible (nil check on only the specifically-mocked functions).
- **Registry-based Func API**: `rewire.Func(t, bar.Greet, replacement)` — user never types mock variable names. Registration is generated directly from mock targets during test compilation.
- **Intrinsic detection**: Parses the Go compiler's own intrinsics.go to detect functions that can't be mocked (replaced by CPU instructions at call sites). Fails with a clear error.
- **`_rewire_mock` variable name**: The wrapper uses `_rewire_mock` as its local variable to avoid shadowing function parameters (e.g., math functions commonly use `f` for float64).
- **Method support**: Methods use `(*Type).Method` / `Type.Method` syntax (matching Go method expressions and `runtime.FuncForPC` naming). Mock variable names include the type: `Mock_Server_Handle`. The mock function receives the receiver as the first argument. Method mocks set via `rewire.Func` are global (all instances share one mock variable); `rewire.InstanceMethod` provides per-receiver scoping via an opt-in `_ByInstance sync.Map` the rewriter only emits when at least one InstanceMethod call references the target.
- **Interface mocks via toolexec (`rewire.NewMock[T]`)**: The sole interface-mock path in rewire. The scanner detects `rewire.NewMock[I]` in test files, the toolexec generator synthesizes a backing struct for I at compile time, and the struct registers itself with the runtime via `RegisterMockFactory[I]`. Same per-instance dispatch as `rewire.InstanceMethod` for concrete methods. Generic interfaces are supported via AST-level type-parameter substitution and per-instantiation factory keys derived from `reflect.TypeFor[I]()`.
- **Composite registry keys via type parameters**: `RegisterMockFactory[I]`, `RegisterByInstance[F]`, and `RegisterReal` derive their lookup keys through reflect at runtime, with the type passed in as a Go type parameter rather than as a pre-formatted string. This is critical for generic interfaces — `runtime.FuncForPC` reports `Container[int].Add` and `Container[string].Add` under the same name (with `[...]` placeholder), so name alone can't disambiguate. The function-type signature does. Both registration and lookup compute the key the same way (`reflect.TypeFor[F]().String()`), so the two sides cannot drift.

## Conventions

- The CLI uses github.com/GiGurra/boa for command definitions.
- AST rewriting generates replacement code as text (fmt.Sprintf + go/parser), not by manually constructing AST nodes.
- Test files (`_test.go`) are never rewritten — only production source files for functions in `rewire.Func` calls.
- Registration files are generated during test compilation and added to the compiler args.
- Generated mock files use `mock_<interfacename>_test.go` naming and are committed to the repo.
