# CLAUDE.md

## Project overview

Rewire is a Go test mocking tool with two complementary features:

1. **Function/method mocking (toolexec)**: Uses `-toolexec` to intercept compilation and rewrite functions at compile time. Scans `_test.go` files for `rewire.Func` calls, builds a targeted list, and rewrites only those during compilation. Production source is never modified.
2. **Interface mock generation (codegen)**: Generates lightweight mock structs for Go interfaces via `rewire mock`. Uses `go:generate` for automation.

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

- `cmd/rewire/main.go` — Entry point. Detects toolexec mode (first arg is absolute path to a Go tool) vs CLI subcommand mode. Uses boa (github.com/GiGurra/boa) for CLI. Subcommands: `rewrite` (debug), `mock` (interface mock generation).
- `pkg/rewire/replace.go` — The public test API:
  - `Func[F any](t, original, replacement)` — recommended API. Uses `runtime.FuncForPC` to resolve function name, looks up mock var pointer in `sync.Map` registry, sets/restores via `reflect`.
  - `Replace[F any](t, &target, replacement)` — low-level API, directly swaps a mock var by pointer.
  - `Register(funcName, mockVarPtr)` — called by generated init() code, not by users.
- `internal/rewriter/rewriter.go` — AST rewriter:
  - `RewriteSource` — rewrites a single named function or method. Accepts `"Func"`, `"(*Type).Method"`, or `"Type.Method"` syntax. Skips bodyless functions.
  - `RewriteAllExported` — rewrites all eligible exported functions (used by the `rewrite` CLI subcommand).
  - `ListExportedFunctions` — returns names of functions eligible for rewriting.
- `internal/mockgen/mockgen.go` — Interface mock generator:
  - `GenerateMock(src, interfaceName, outputPkg)` — parses interface from source, generates mock struct with function fields for each method. Handles imported types, variadic params, unnamed params, multiple returns. Uses named return params for zero-value defaults.
  - `InferPackageName(src)` — extracts package name from Go source.
- `internal/toolexec/toolexec.go` — Toolexec wrapper:
  - Intercepts `compile` invocations for any package with targeted functions.
  - Rewrites only the specific functions found in `rewire.Func` calls.
  - For test compilations: generates `_rewire_init_test.go` that registers mock var pointers.
- `internal/toolexec/scan.go` — Pre-scans `_test.go` files for `rewire.Func` calls. Builds a map of import path -> function/method names. Handles `pkg.Func`, `pkg.Type.Method`, and `(*pkg.Type).Method` patterns. Results cached per build (keyed on parent PID).
- `internal/toolexec/intrinsics.go` — Detects compiler intrinsic functions by parsing `$GOROOT/src/cmd/compile/internal/ssagen/intrinsics.go`. Intrinsics are replaced by CPU instructions at the call site, bypassing any wrapper.
- `example/` — End-to-end examples:
  - `bar/bar.go` — production functions and types (`Greet`, `Greeter`)
  - `bar/interfaces.go` — interfaces for mock generation (`GreeterIface`, `Store`, `Logger`, `HTTPClient`)
  - `foo/` — tests using both `rewire.Func` (function/method/stdlib mocking) and generated interface mocks (with `go:generate` directives)

## Key design decisions

- **Targeted rewriting**: Rewire pre-scans `_test.go` files for `rewire.Func` calls and only rewrites those specific functions. This solved the chicken-and-egg problem (dependencies compile before test packages) by doing a file walk + AST parse upfront.
- **toolexec over -overlay**: toolexec integrates with `go test` directly and handles per-package compilation naturally.
- **Separate test cache**: Recommended setup uses `GOCACHE=$HOME/.cache/rewire-test` for tests, keeping production build cache clean. If GOFLAGS is set globally instead, the overhead is negligible (nil check on only the specifically-mocked functions).
- **Registry-based Func API**: `rewire.Func(t, bar.Greet, replacement)` — user never types mock variable names. Registration is generated directly from mock targets during test compilation.
- **Intrinsic detection**: Parses the Go compiler's own intrinsics.go to detect functions that can't be mocked (replaced by CPU instructions at call sites). Fails with a clear error.
- **`_rewire_mock` variable name**: The wrapper uses `_rewire_mock` as its local variable to avoid shadowing function parameters (e.g., math functions commonly use `f` for float64).
- **Method support**: Methods use `(*Type).Method` / `Type.Method` syntax (matching Go method expressions and `runtime.FuncForPC` naming). Mock variable names include the type: `Mock_Server_Handle`. The mock function receives the receiver as the first argument. Method mocks are global (all instances share one mock variable).
- **Interface mocks are codegen, not toolexec**: Interface mock generation uses `rewire mock` + `go:generate` rather than compile-time generation. This gives IDE support (gopls sees the generated types), reviewable diffs, and follows standard Go patterns.

## Conventions

- The CLI uses github.com/GiGurra/boa for command definitions.
- AST rewriting generates replacement code as text (fmt.Sprintf + go/parser), not by manually constructing AST nodes.
- Test files (`_test.go`) are never rewritten — only production source files for functions in `rewire.Func` calls.
- Registration files are generated during test compilation and added to the compiler args.
- Generated mock files use `mock_<interfacename>_test.go` naming and are committed to the repo.
