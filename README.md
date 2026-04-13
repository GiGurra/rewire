# rewire

[![CI Status](https://github.com/GiGurra/rewire/actions/workflows/ci.yml/badge.svg)](https://github.com/GiGurra/rewire/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/GiGurra/rewire)](https://goreportcard.com/report/github.com/GiGurra/rewire)
[![Docs](https://img.shields.io/badge/docs-gigurra.github.io%2Frewire-blue)](https://gigurra.github.io/rewire/)

> **Experimental** — this project is in early development. Both the implementation and APIs may change at any time. Use at your own risk.

A complete mocking solution for Go:

- **Replace any function or method at test time** — package-level functions, struct methods, stdlib, third-party. No interfaces, no dependency injection, no unsafe runtime patches. This is what other mocking libraries can't do.
- **Generate mock structs for interfaces** — for traditional dependency-injection style testing, like other Go mocking libraries.

One tool, both approaches. Production code on disk is never modified — rewire intercepts the Go compiler via `-toolexec` and emits what's needed only in-memory during compilation. This covers function/method mocking (rewritten wrappers around the targeted functions) and, as of Phase 1, interface mocks via `rewire.NewMock[T]` — no `go:generate`, no committed mock files. An older CLI mock generator is still available for users who prefer committed mock source.

## Quick start

```bash
# Install the rewire binary
go install github.com/GiGurra/rewire/cmd/rewire@latest

# Add the test library to your module
go get github.com/GiGurra/rewire/pkg/rewire

# Clean the Go build cache (needed once, so rewire can rewrite cached packages)
go clean -cache

# Run tests with rewire (for function/method mocking)
GOFLAGS="-toolexec=rewire" go test ./...
```

<details>
<summary><strong>Function mocking</strong> — replace any function at test time, no code changes required</summary>

Here's a fully self-contained example — no production code to set up, just stdlib. We mock `os.Getwd` and then call `filepath.Abs`, which internally calls `os.Getwd` to resolve a relative path:

```go
package foo

import (
    "os"
    "path/filepath"
    "testing"

    "github.com/GiGurra/rewire/pkg/rewire"
)

func TestFilepathAbs_WithMockedOsGetwd(t *testing.T) {
    rewire.Func(t, os.Getwd, func() (string, error) {
        return "/mocked", nil
    })

    got, _ := filepath.Abs("foo")
    // got == "/mocked/foo"
}
```

Notice what's happening: `filepath.Abs` lives in `path/filepath`, it calls `os.Getwd` which lives in `os`, and neither package belongs to your project. Rewire rewrites `os.Getwd` at compile time, so when `filepath.Abs` reaches the call site, it gets the mocked version. No interfaces, no dependency injection, no wrappers.

It works the same way on your own code:

```go
// bar/bar.go — never modified
package bar

func Greet(name string) string {
    return "Hello, " + name + "!"
}
```

```go
// foo/foo.go — never modified
package foo

import "example/bar"

func Welcome(name string) string {
    return "Welcome! " + bar.Greet(name)
}
```

```go
// foo/foo_test.go
func TestWelcome_WithMock(t *testing.T) {
    rewire.Func(t, bar.Greet, func(name string) string {
        return "Howdy, " + name
    })

    got := Welcome("Alice")
    // got == "Welcome! Howdy, Alice"
    // Welcome still calls bar.Greet as normal — but bar.Greet now runs the mock.
}

func TestWelcome_Real(t *testing.T) {
    // No mock here. Welcome("Bob") == "Welcome! Hello, Bob!"
    // Mocks are per-test; the previous test does not leak.
}
```

Pass the original function and its replacement. No mock variable names, no generated types, no interface wrappers. Mocks are automatically restored after each test via `t.Cleanup`.

**Generic functions** — pass the instantiation you want to mock, and only that instantiation is replaced:

```go
rewire.Func(t, bar.Map[int, string], func(in []int, f func(int) string) []string {
    return []string{"mocked"}
})
// bar.Map[float64, bool] still runs the real implementation in the same test
```

**Spy pattern** — use `rewire.Real` to capture the pre-rewrite implementation and delegate to it from inside your mock:

```go
realGreet := rewire.Real(t, bar.Greet)

rewire.Func(t, bar.Greet, func(name string) string {
    return realGreet(name) + " [wrapped]"
})
```

**Mid-test cleanup** — `rewire.Restore(t, fn)` ends a mock early if you want the real implementation back before the test finishes. Safe to call multiple times.

Requires `GOFLAGS="-toolexec=rewire"` to be set (see [Setup](#recommended-test-specific-environment)).

</details>

<details>
<summary><strong>Method mocking</strong> — replace struct methods using Go method expression syntax</summary>

```go
func TestGreetWith_MockedMethod(t *testing.T) {
    rewire.Func(t, (*bar.Greeter).Greet, func(g *bar.Greeter, name string) string {
        return "Mocked, " + name
    })
    // All calls to (*Greeter).Greet use the mock in this test
}
```

Both pointer (`(*Type).Method`) and value (`Type.Method`) receivers are supported. The replacement function receives the receiver as its first argument.

Note: method mocks apply to **all instances** of the type, not a specific object. This is consistent with how function mocking works — the mock variable is package-level.

Requires `GOFLAGS="-toolexec=rewire"` to be set (see [Setup](#recommended-test-specific-environment)).

</details>

<details>
<summary><strong>Interface mocks</strong> — `rewire.NewMock[T]` (no go:generate, no committed files)</summary>

Rewire ships two styles of interface mocking. The newer one — `rewire.NewMock[T]` — synthesizes the backing struct at compile time via toolexec. **No `go:generate`, no committed mock files.** Just reference the interface in a test and it works:

```go
func TestService_GreetingFlow(t *testing.T) {
    greeter := rewire.NewMock[bar.GreeterIface](t)

    rewire.InstanceMethod(t, greeter, bar.GreeterIface.Greet, func(g bar.GreeterIface, name string) string {
        return "mocked: " + name
    })

    svc := NewService(greeter)
    got := svc.HelloFlow("Alice")
    // ...
}
```

Two mocks of the same interface are scoped independently — stubs on one don't leak to the other. Unstubbed methods return zero values. `rewire.Restore(t, mock)` clears every stub on a mock.

For tests that need multi-pattern stubbing, argument predicates, call-count bounds, or automatic "was this actually called?" verification, the [expectation DSL](docs/expectations.md) has an `expect.ForInstance(t, mock, bar.GreeterIface.Greet)` entry point that wraps `NewMock` mocks (and per-instance concrete methods) with the same rule-builder API as `expect.For`.

The toolexec wrapper scans test files for `rewire.NewMock[X]` references, parses the interface's source, and emits a backing struct into the test package's compile args. You never see the generated code and never commit it — it's the same mechanism rewire uses to emit method-mock wrappers and per-instance dispatch tables.

**Current scope (Phase 1):** non-generic interfaces, methods using builtin or already-qualified types. Embedded interfaces, types from the interface's own declaring package, and generic interfaces are Phase 2+ items (rejected with a clear error for now).

See [Interface Mocks](docs/interface-mocks.md) for the full feature set and the older `rewire mock` CLI.

> **Note on the older CLI.** Rewire also supports a traditional `rewire mock` CLI, typically invoked via `go:generate`, that produces a committed `mock_*_test.go` file. It's still fully supported, but once the `rewire.NewMock[T]` path reaches feature parity (embedded interfaces, same-package types, generics) rewire's own CLI generator is a candidate for deprecation. This is purely about rewire's internal surface area — it's not a statement about the `go:generate` ecosystem in general.

</details>

<details>
<summary><strong>How it works</strong> — toolexec compile-time rewriting</summary>

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

</details>

<details>
<summary><strong>Setup</strong> — IDE and terminal configuration for toolexec</summary>

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

</details>

<details>
<summary><strong>Limitations</strong> — function/method mocking (toolexec)</summary>

These limitations apply to compile-time function/method mocking only, not interface mock generation. For per-instance method stubs (different behavior per object) rewire ships `rewire.InstanceMethod` — see [Per-instance method mocks](https://gigurra.github.io/rewire/method-mocking/#per-instance-method-mocks).

- **Compiler intrinsics** — functions like `math.Abs`, `math.Sqrt`, `math.Floor` are replaced with CPU instructions by the compiler. Rewire detects these and fails with a clear error. Use non-intrinsic alternatives (e.g., `math.Pow` works fine).
- **No parallel mock safety** — parallel tests in the same package should not mock the same function with different replacements. The mock variable is shared across parallel goroutines.
- **Bodyless functions** — functions implemented in assembly (no Go body) cannot be rewritten.

</details>

## Acknowledgements

This project is 100% vibe coded — AST rewriting and compiler toolchains are way outside my comfort zone. Built entirely with [Claude Code](https://claude.ai).

Inspired by Erlang's [meck](https://github.com/eproxus/meck), although the underlying mechanism is entirely different.

## License

MIT
