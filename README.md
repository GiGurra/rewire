# rewire

> **Experimental** — this project is in early development. Both the implementation and APIs may change at any time. Use at your own risk.

A complete mocking solution for Go:

- **Replace any function or method at test time** — package-level functions, struct methods, stdlib, third-party. No interfaces, no dependency injection, no unsafe runtime patches. This is what other mocking libraries can't do.
- **Generate mock structs for interfaces** — for traditional dependency-injection style testing, like other Go mocking libraries.

One tool, both approaches. Production source stays **100% clean** — rewire intercepts the Go compiler via `-toolexec` and rewrites only the specific functions you mock. Source on disk is never modified.

## Quick start

```bash
# Install
go install github.com/GiGurra/rewire/cmd/rewire@latest

# Clean the Go build cache (needed once, so rewire can rewrite cached packages)
go clean -cache

# Run tests with rewire (for function/method mocking)
GOFLAGS="-toolexec=rewire" go test ./...
```

## Function mocking (toolexec)

Replace any function at test time — no code changes required.

Given production code:

```go
// bar/bar.go — never modified
package bar

func Greet(name string) string {
    return "Hello, " + name + "!"
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

### Mocking methods

Methods work too — use Go's method expression syntax:

```go
func TestGreetWith_MockedMethod(t *testing.T) {
    rewire.Func(t, (*bar.Greeter).Greet, func(g *bar.Greeter, name string) string {
        return "Mocked, " + name
    })
    // All calls to (*Greeter).Greet use the mock in this test
}
```

Note: method mocks apply to **all instances** of the type, not a specific object. This is consistent with how function mocking works — the mock variable is package-level.

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

## Interface mock generation

For interfaces you pass in (dependency injection), rewire generates lightweight mock structs:

```bash
rewire mock -f bar.go -i Store -p foo -o mock_store_test.go
```

This generates a struct with function fields for each method:

```go
type MockStore struct {
    GetFunc    func(key string) (string, error)
    SetFunc    func(key string, value string) error
    DeleteFunc func(key string) error
}

func (m *MockStore) Get(key string) (_r0 string, _r1 error) {
    if m.GetFunc != nil {
        return m.GetFunc(key)
    }
    return // zero values
}
// ...
```

Use in tests:

```go
func TestGetOrDefault(t *testing.T) {
    mock := &MockStore{
        GetFunc: func(key string) (string, error) {
            return "value", nil
        },
    }
    got := GetOrDefault(mock, "key", "default")
    // got == "value"
}
```

### go:generate workflow

Add directives to your test files and regenerate with `go generate`:

```go
//go:generate rewire mock -f ../bar/interfaces.go -i Store -p foo -o mock_store_test.go
//go:generate rewire mock -f ../bar/interfaces.go -i Logger -p foo -o mock_logger_test.go
```

```bash
go generate ./...   # regenerate mocks
go test ./...       # run tests
```

Handles imported types (`context.Context`, `*http.Request`, `io.Reader`, etc.), variadic parameters, unnamed parameters, and multiple return values.

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
    return "Hello, " + name + "!"
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

This is simpler but means `go build` also rewrites targeted functions. The overhead is probably negligible in most situations — only functions you explicitly mock are affected, and it's just a nil check.

## Test isolation

Each `go test` package compiles into a separate binary:
- `foo`'s tests can mock `bar.Greet`
- `baz`'s tests can use the real `bar.Greet`
- No configuration needed — each test binary is independent

Within a test package, `rewire.Func` uses `t.Cleanup` to restore the original after each test.

## Limitations

- **Compiler intrinsics** — functions like `math.Abs`, `math.Sqrt`, `math.Floor` are replaced with CPU instructions by the compiler. Rewire detects these and fails with a clear error. Use non-intrinsic alternatives (e.g., `math.Pow` works fine).
- **Method mocks are global** — method mocks apply to all instances of a type, not per-object. This is consistent with function mocking.
- **No generics** — generic functions are skipped
- **No parallel mock safety** — parallel tests in the same package should not mock the same function with different replacements
- **Bodyless functions** — functions implemented in assembly (no Go body) cannot be rewritten

## Acknowledgements

This project is 100% vibe coded — AST rewriting and compiler toolchains are way outside my comfort zone. Built entirely with [Claude Code](https://claude.ai/claude-code).

Inspired by Erlang's [meck](https://github.com/eproxus/meck), although the mechanism is entirely different.

## License

MIT
