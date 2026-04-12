# Function Mocking

Replace any package-level function at test time using `rewire.Func`. No interfaces, no dependency injection — the production code stays untouched.

## Basic usage

```go
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
    // bar.Greet returns "Howdy, Alice"
}

func TestWelcome_Real(t *testing.T) {
    // bar.Greet uses the real implementation — mocks are per-test
}
```

The three arguments to `rewire.Func`:

1. `t` — the test context (used for automatic cleanup)
2. `bar.Greet` — the original function to replace
3. The replacement — must have the same signature

The mock is automatically restored after the test via `t.Cleanup`.

## Mocking stdlib and third-party packages

Rewire works with any package, not just your own module:

```go
func TestSquareRoot(t *testing.T) {
    rewire.Func(t, math.Pow, func(x, y float64) float64 {
        return 42
    })
    // math.Pow now returns 42 in this test
}
```

This works because rewire intercepts the compiler — it can rewrite functions in any package that gets compiled.

## Closure capture

The replacement function is a regular Go closure, so it can capture variables from the test scope:

```go
func TestGreet_CallTracking(t *testing.T) {
    callCount := 0
    var lastArg string

    rewire.Func(t, bar.Greet, func(name string) string {
        callCount++
        lastArg = name
        return "counted"
    })

    Welcome("Alice")
    Welcome("Bob")

    if callCount != 2 {
        t.Errorf("expected 2 calls, got %d", callCount)
    }
    if lastArg != "Bob" {
        t.Errorf("expected last arg %q, got %q", "Bob", lastArg)
    }
}
```

This lets you track calls, record arguments, return different values based on input, etc. — all without any special mocking framework API.

## Test isolation

Each `go test` package compiles into a separate test binary:

- Package `foo`'s tests can mock `bar.Greet`
- Package `baz`'s tests use the real `bar.Greet`
- No configuration needed — each binary is independent

Within a test package, `rewire.Func` uses `t.Cleanup` to restore the original after each test. Tests run sequentially by default, so mocks don't interfere with each other.

## Requirements

Function mocking requires the toolexec wrapper to be active:

```bash
GOFLAGS="-toolexec=rewire" go test ./...
```

See [Setup](setup.md) for IDE and terminal configuration.

## What can't be mocked

See [Limitations](limitations.md) for details on compiler intrinsics, generic functions, and other edge cases.
