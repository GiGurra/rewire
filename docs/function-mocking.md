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

The same API works on functions in your own module. Production code:

```go
// example/bar/bar.go
package bar

func Greet(name string) string {
    return "Hello, " + name + "!"
}
```

```go
// example/foo/foo.go
package foo

import "example/bar"

func Welcome(name string) string {
    return "Welcome! " + bar.Greet(name)
}
```

Test:

```go
// example/foo/foo_test.go
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
    want := "Welcome! Howdy, Alice"
    if got != want {
        t.Errorf("got %q, want %q", got, want)
    }
}

func TestWelcome_Real(t *testing.T) {
    // No mock here — bar.Greet runs its real body, so:
    //   Welcome("Bob") → "Welcome! Hello, Bob!"
    // Mocks are per-test; the previous TestWelcome_WithMock does not leak.
}
```

`Welcome` never changes, and `bar.Greet` never changes on disk. At compile time rewire rewrites `bar.Greet` so it checks a package-level mock variable first — when the mock is set, `Welcome` transparently sees the replacement instead.

## Spying: delegating to the real implementation

Sometimes you want the mock to run *in addition to* the real function, not *instead of* it — for counting calls, adding audit behavior, or wrapping the real output. `rewire.Real` returns the pre-rewrite implementation so you can call it from inside your mock:

```go
func TestBarGreet_WithWrapping(t *testing.T) {
    realGreet := rewire.Real(t, bar.Greet)

    rewire.Func(t, bar.Greet, func(name string) string {
        return realGreet(name) + " [wrapped]"
    })

    got := Welcome("Alice")
    // Welcome("Alice") → bar.Greet runs the mock, which calls realGreet,
    // which runs the original body, so got == "Welcome! Hello, Alice! [wrapped]"
}
```

This is the same idea as Mockito's spy pattern. A few properties worth knowing:

- **Order doesn't matter.** You can call `rewire.Real` before or after `rewire.Func`, and even from *inside* the mock closure — the returned function value is always the real implementation, never the wrapper.
- **It works for methods too.** Pass a method expression: `rewire.Real(t, (*bar.Greeter).Greet)` returns a `func(*bar.Greeter, string) string` that invokes the real method when called with a receiver.

## Generic functions

Generic functions work with the same API — pass the instantiation you want to mock as the argument to `rewire.Func`, and rewire replaces only that instantiation:

```go
// Production: example/bar/bar.go
func Map[T, U any](in []T, f func(T) U) []U {
    out := make([]U, len(in))
    for i, v := range in {
        out[i] = f(v)
    }
    return out
}
```

```go
// Test: example/foo/foo_test.go
func TestMap_MockOnlyIntString(t *testing.T) {
    rewire.Func(t, bar.Map[int, string], func(in []int, f func(int) string) []string {
        return []string{"mocked"}
    })

    // bar.Map[int, string] now returns ["mocked"] regardless of input
    got := bar.Map([]int{1, 2, 3}, func(x int) string { return "real" })
    // got == ["mocked"]

    // bar.Map[float64, bool] is untouched — still runs the real body
    got2 := bar.Map([]float64{1, 2}, func(x float64) bool { return x > 0 })
    // got2 == [true, true]
}
```

Mocks are **per-instantiation**: mocking `Map[int, string]` does not affect `Map[float64, bool]` or any other type-argument combination. You can mock multiple instantiations of the same function in a single test and each gets its own independent replacement.

`rewire.Real` and `rewire.Restore` work the same way:

```go
// Spy on a specific instantiation
realMap := rewire.Real(t, bar.Map[int, string])
rewire.Func(t, bar.Map[int, string], func(in []int, f func(int) string) []string {
    out := realMap(in, f)
    for i := range out {
        out[i] += "!"
    }
    return out
})
```

**Known limitations:**

- Generic *methods* on generic types aren't supported yet — the rewriter will reject them with a clear error. Plain generic functions work.
- rewire relies on the pre-scan seeing every instantiation your tests will use. An instantiation that only appears inside a `reflect.MakeFunc` or similar dynamic construct wouldn't be picked up, but that's a niche case.

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
