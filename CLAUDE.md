# CLAUDE.md

## Project overview

Rewire is a Go test mocking tool that uses `-toolexec` to intercept compilation and rewrite functions at compile time. Production source is never modified — mock variables only exist in the compiled test binaries. Inspired by Erlang's meck.

## Build and test

```bash
# Build
go build ./...

# Run all tests (include toolexec for the example tests)
go clean -cache && GOFLAGS="-toolexec=rewire" go test ./...

# Run just the rewriter unit tests (no toolexec needed)
go test ./internal/rewriter/

# Install the binary (needed before toolexec can work)
go install ./cmd/rewire/
```

Important: after changing rewire source, always `go install ./cmd/rewire/` before running tests with toolexec, since the toolexec binary must be up to date.

## Project structure

- `cmd/rewire/main.go` — Entry point. Detects toolexec mode (first arg is absolute path to a Go tool) vs CLI subcommand mode. Uses boa (github.com/GiGurra/boa) for CLI framework.
- `pkg/rewire/replace.go` — The public test API:
  - `Func[F any](t, original, replacement)` — recommended API. Uses `runtime.FuncForPC` to resolve function name, looks up mock var pointer in a `sync.Map` registry, sets/restores via `reflect`.
  - `Replace[F any](t, &target, replacement)` — low-level API that directly swaps a mock var by pointer.
  - `Register(funcName, mockVarPtr)` — called by generated init() code, not by users.
- `internal/rewriter/rewriter.go` — AST rewriter:
  - `RewriteSource` — rewrites a single named function.
  - `RewriteAllExported` — rewrites all eligible exported functions in a file. Skips methods, generics, and already-rewritten functions (idempotent).
  - `ListExportedFunctions` — returns names of functions eligible for rewriting (used by toolexec for registration generation).
- `internal/toolexec/toolexec.go` — Toolexec wrapper:
  - Intercepts `compile` invocations for same-module packages.
  - Rewrites source files to temp dir, passes modified args to the real compiler.
  - For test compilations (detected by presence of `_test.go` files): generates a `_rewire_init_test.go` registration file that maps function names to mock var pointers via `rewire.Register`.
- `example/` — End-to-end example: `bar` has a clean `Greet` function, `foo` calls it, `foo_test.go` mocks it with `rewire.Func`.

## Key design decisions

- **toolexec over -overlay**: toolexec integrates with `go test` directly and handles per-package compilation naturally.
- **Rewrite all exported functions**: Rather than scanning test files to determine which functions need mocking (chicken-and-egg: dependencies compile before the test package), we blanket-rewrite all exported functions in same-module packages. The cost is a nil check per function call.
- **GOFLAGS for IDE integration**: `export GOFLAGS="-toolexec=rewire"` makes IntelliJ/GoLand click-to-run work without any plugin.
- **Registry-based Func API**: `rewire.Func(t, bar.Greet, replacement)` — user never types mock variable names. The toolexec generates a registration init() in each test binary that maps function names (via `runtime.FuncForPC`) to mock var pointers. `rewire.Func` looks up the registry at runtime.
- **Mock_ naming convention**: Generated variables are named `Mock_<FuncName>`. These are an internal implementation detail — users interact via `rewire.Func` which hides them.
- **Build cache**: switching between toolexec/non-toolexec requires `go clean -cache`. Error messages include GOFLAGS state and step-by-step fix instructions.

## Conventions

- The CLI uses github.com/GiGurra/boa for command definitions.
- AST rewriting generates replacement code as text (using fmt.Sprintf + go/parser), not by manually constructing AST nodes. This is more readable and maintainable.
- Test files (`_test.go`) are never rewritten by toolexec — only production source files get mock wrappers.
- Registration files (`_rewire_init_test.go`) are generated and added to test compilations only.
