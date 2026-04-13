# Function Mocking

Replace any package-level function at test time using `rewire.Func`. No interfaces, no dependency injection — the production code stays untouched.

## Basic usage

Here's a fully self-contained example — no production code to set up, just stdlib. We mock `os.Getwd` and then call `filepath.Abs`, which internally calls `os.Getwd` to resolve a relative path:

```go
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

`filepath.Abs` lives in `path/filepath`, calls `os.Getwd` which lives in `os`, and neither package belongs to your project. Rewire rewrites `os.Getwd` at compile time, so when `filepath.Abs` reaches the call site, it gets the mocked version.

The three arguments to `rewire.Func`:

1. `t` — the test context (used for automatic cleanup)
2. `os.Getwd` — the original function to replace
3. The replacement — must have the same signature

The mock is automatically restored after the test via `t.Cleanup`.

## Mocking your own code

The same API works on functions in your own module:

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
    // Welcome internally calls bar.Greet, which now returns "Howdy, Alice"
}

func TestWelcome_Real(t *testing.T) {
    // bar.Greet uses the real implementation — mocks are per-test
}
```

## Restoring mocks mid-test

Mocks are normally restored automatically when the test ends (via `t.Cleanup`). If you need to end a mock earlier — for example, mock a dependency during setup but run the actual test body against the real implementation — call `rewire.Restore`:

```go
func TestFixtureSetupThenRealCall(t *testing.T) {
    rewire.Func(t, os.Getwd, func() (string, error) {
        return "/fixture", nil
    })

    // ... setup code that depends on os.Getwd returning /fixture ...

    rewire.Restore(t, os.Getwd)

    // ... test body now sees the real os.Getwd ...
}
```

`Restore` is idempotent — you can call it any number of times, and it's safe to call even when no mock is currently active. The automatic cleanup installed by `Func` still runs correctly afterwards.

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
