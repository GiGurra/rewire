# rewire

Compile-time function mocking for Go. Replace any function during tests — no interfaces, no dependency injection, no unsafe runtime patches.

Production source stays **100% clean**. Rewire intercepts the Go compiler via `-toolexec`, scans your test files for `rewire.Func` calls, and rewrites only those specific functions on the fly. Source on disk is never modified.

Inspired by Erlang's [meck](https://github.com/eproxus/meck).

## Quick start

```bash
# Install
go install github.com/GiGurra/rewire/cmd/rewire@latest

# Run tests with rewire
GOFLAGS="-toolexec=rewire" go test ./...
```

## Usage

Given production code:

```go
// bar/bar.go — never modified
package bar

func Greet(name string) string {
    return fmt.Sprintf("Hello, %s!", name)
}
```

Mock it in tests:

```go
// foo/foo_test.go
package foo

import (
    "testing"
    "example/bar"
    "github.com/GiGurra/rewire/pkg/rewire"
)

func TestWelcome_WithMock(t *testing.T) {
    rewire.Func(t, bar.Greet, func(name string) string {
        return "Howdy, " + name
    })

    got := Welcome("Alice")
    // bar.Greet returns "Howdy, Alice" — restored automatically after test
}

func TestWelcome_Real(t *testing.T) {
    // bar.Greet uses the real implementation here
}
```

Pass the original function and its replacement. No mock variable names, no generated types, no interface wrappers.

### Mocking stdlib and external packages

Rewire works with any package, not just your own:

```go
func TestSquareRoot(t *testing.T) {
    rewire.Func(t, math.Pow, func(x, y float64) float64 {
        return 42
    })
    // math.Pow now returns 42 in this test
}
```

## How it works

1. **Pre-scan** — rewire scans `_test.go` files in your module for `rewire.Func` calls and builds a target list (e.g., `bar.Greet`, `math.Pow`)
2. **Targeted rewrite** — when the compiler processes a package containing targeted functions, rewire rewrites only those functions with a `Mock_` variable and nil-check wrapper
3. **Registration** — when compiling a test package, rewire generates an `init()` that registers mock variable pointers in a runtime registry
4. **Runtime swap** — `rewire.Func` uses `runtime.FuncForPC` to resolve the function name, looks up the mock variable pointer, and swaps it via `reflect`. `t.Cleanup` restores the original

Only functions explicitly listed in `rewire.Func` calls are rewritten. Everything else passes through untouched.

The rewrite transformation (only exists during compilation):

```go
var Mock_Greet func(name string) string

func Greet(name string) string {
    if _rewire_mock := Mock_Greet; _rewire_mock != nil {
        return _rewire_mock(name)
    }
    return _real_Greet(name)
}

func _real_Greet(name string) string {
    return fmt.Sprintf("Hello, %s!", name)
}
```

## Setup

### Recommended: test-specific environment

Keep test builds in a separate cache so `go build` and `go test` never interfere:

**Terminal (alias in shell profile):**
```bash
alias gotest='GOFLAGS="-toolexec=rewire" GOCACHE="$HOME/.cache/rewire-test" go test'
```

**GoLand:** Run > Edit Configurations > Templates > Go Test > Environment variables:
```
GOFLAGS=-toolexec=rewire
GOCACHE=/Users/<you>/.cache/rewire-test
```

**VS Code (settings.json):**
```json
"go.testEnvVars": {
    "GOFLAGS": "-toolexec=rewire",
    "GOCACHE": "${env:HOME}/.cache/rewire-test"
}
```

With this setup:
- `go build` uses the default cache — clean production binaries, no rewire artifacts
- `go test` (via alias or IDE) uses a separate cache — rewire active, no cache conflicts

### Alternative: global GOFLAGS

If you don't mind the minimal overhead (a nil check per mocked function in production builds):

```bash
export GOFLAGS="-toolexec=rewire"
```

This is simpler but means `go build` also rewrites targeted functions. The overhead is negligible — only functions you explicitly mock are affected, and the nil check is ~0 cost.

## Test isolation

Each `go test` package compiles into a separate binary:
- `foo`'s tests can mock `bar.Greet`
- `baz`'s tests can use the real `bar.Greet`
- No configuration needed — each test binary is independent

Within a test package, `rewire.Func` uses `t.Cleanup` to restore the original after each test.

## Limitations

- **Compiler intrinsics** — functions like `math.Abs`, `math.Sqrt`, `math.Floor` are replaced with CPU instructions by the compiler. Rewire detects these and fails with a clear error. Use non-intrinsic alternatives (e.g., `math.Pow` works fine).
- **No methods** — only package-level functions (method support is planned)
- **No generics** — generic functions are skipped
- **No parallel mock safety** — parallel tests in the same package should not mock the same function with different replacements
- **Bodyless functions** — functions implemented in assembly (no Go body) cannot be rewritten

## Project structure

```
cmd/rewire/              CLI entry point (toolexec mode + rewrite subcommand)
pkg/rewire/              Test helper library (Func and Replace)
internal/rewriter/       AST-based source rewriter
internal/toolexec/       Toolexec wrapper, test file scanner, intrinsic detection
example/                 End-to-end examples (same-module + stdlib mocking)
docs/                    Design docs and decision log
```

## License

MIT
