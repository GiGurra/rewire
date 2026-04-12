# CLAUDE.md

## Project overview

Rewire is a Go test mocking tool that uses `-toolexec` to intercept compilation and rewrite functions at compile time. Production source is never modified — mock variables only exist in the compiled test binaries.

## Build and test

```bash
# Build
go build ./...

# Run all tests (include toolexec for the example tests)
go test -toolexec=rewire ./...

# Run just the rewriter unit tests (no toolexec needed)
go test ./internal/rewriter/

# Install the binary (needed for toolexec to work)
go install ./cmd/rewire/
```

## Project structure

- `cmd/rewire/main.go` — Entry point. Detects toolexec mode (first arg is absolute path) vs CLI subcommand mode. Uses boa (github.com/GiGurra/boa) for CLI framework.
- `pkg/rewire/replace.go` — The public test API. `Replace[F any](t, &target, replacement)` swaps a mock var and restores on cleanup.
- `internal/rewriter/rewriter.go` — AST rewriter. `RewriteSource` rewrites a single named function. `RewriteAllExported` rewrites all eligible exported functions in a file. Skips methods, generics, and already-rewritten functions.
- `internal/toolexec/toolexec.go` — Toolexec wrapper. Intercepts `compile` invocations, determines if the package is in the same module, rewrites source files to temp dir, passes modified args to the real compiler.
- `example/` — End-to-end example: `bar` has a clean `Greet` function, `foo` calls it, `foo_test.go` mocks it.

## Key design decisions

- **toolexec over -overlay**: toolexec integrates with `go test` directly and handles per-package compilation naturally.
- **Rewrite all exported functions**: Rather than scanning test files to determine which functions need mocking (chicken-and-egg problem with compilation order), we blanket-rewrite all exported functions in same-module packages. The cost is a nil check per function call.
- **GOFLAGS for IDE integration**: `export GOFLAGS="-toolexec=rewire"` makes IntelliJ/GoLand click-to-run work without any plugin.
- **Mock_ naming convention**: Generated variables are named `Mock_<FuncName>`. This is visible to test code but not in production source.

## Conventions

- The CLI uses github.com/GiGurra/boa for command definitions.
- AST rewriting generates replacement code as text (using fmt.Sprintf + go/parser), not by manually constructing AST nodes. This is more readable and maintainable.
- Test files (`_test.go`) are never rewritten by toolexec — only production source files.
