# Getting Started

## Installation

You need two things: the rewire binary (for toolexec and mock generation) and the library (for `rewire.Func` in tests).

```bash
# Install the rewire binary
go install github.com/GiGurra/rewire/cmd/rewire@latest

# Add the test library to your module
go get github.com/GiGurra/rewire/pkg/rewire
```

## Function mocking (quick start)

### 1. Clean the build cache

Rewire needs to recompile packages through its toolexec wrapper. Clear the cache once so cached packages get recompiled:

```bash
go clean -cache
```

### 2. Write a test using `rewire.Func`

```go
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
    if got != "Welcome! Howdy, Alice" {
        t.Errorf("got %q", got)
    }
}
```

### 3. Run with toolexec

```bash
GOFLAGS="-toolexec=rewire" go test ./...
```

That's it. `bar.Greet` is replaced for the duration of `TestWelcome_WithMock` and automatically restored after.

See [Setup](setup.md) for IDE configuration (GoLand, VS Code) and recommended cache strategies.

## Method mocking (quick start)

Method mocks work exactly like function mocks — pass a Go method expression as the target:

```go
func TestGreetWith_MockedMethod(t *testing.T) {
    rewire.Func(t, (*bar.Greeter).Greet, func(g *bar.Greeter, name string) string {
        return "Mocked, " + name
    })

    g := &bar.Greeter{Prefix: "Hi"}
    // GreetWith now sees the mocked method on every *bar.Greeter instance.
}
```

Both pointer (`(*Type).Method`) and value (`Type.Method`) receivers work. The replacement function takes the receiver as its first parameter.

For **per-instance** scoping — mock one specific receiver while other instances run the real method body — use `rewire.InstanceMethod`:

```go
s1 := &bar.Server{Name: "primary"}
s2 := &bar.Server{Name: "secondary"}

rewire.InstanceMethod(t, s1, (*bar.Server).Handle, func(s *bar.Server, req string) string {
    return "primary-mock: " + req
})

s1.Handle("ping") // "primary-mock: ping"
s2.Handle("ping") // real Handle body
```

See [Method Mocking](method-mocking.md) for global and per-instance details including generic methods.

## Interface mocking (quick start)

Rewire synthesizes the backing struct for an interface at compile time. No `go:generate` step, no committed mock files — just reference the interface in a test:

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

The toolexec wrapper scans test files for `rewire.NewMock[X]` references, parses the interface's source, and emits a concrete backing struct into the test package's compile args. You don't see the generated code, don't commit it, and don't regenerate it when the interface changes.

Two mocks of the same interface are scoped independently via the same per-instance dispatch that backs `rewire.InstanceMethod`. Unstubbed methods return zero values. `rewire.Restore(t, mock)` clears every stub bound to a mock.

See [Interface Mocks](interface-mocks.md) for the full feature set, including generic interfaces.

## Expectation DSL (optional)

For tests that need multi-pattern stubs, argument predicates, call-count verification, or async synchronization, the `expect` package layers a fluent DSL on top of any rewire mock:

```go
import "github.com/GiGurra/rewire/pkg/rewire/expect"

e := expect.For(t, bar.Greet)
e.On("Alice").Returns("hi Alice")
e.Match(func(name string) bool { return strings.HasPrefix(name, "admin_") }).Returns("admin")
e.OnAny().Returns("hi other")
```

Use `expect.ForInstance(t, mock, target)` to get the same DSL for per-instance method mocks and interface methods on `NewMock` instances. See [Expectations DSL](expectations.md).

## Next steps

- [Function Mocking](function-mocking.md) — detailed guide with examples
- [Method Mocking](method-mocking.md) — global + per-instance method mocks
- [Interface Mocks](interface-mocks.md) — `NewMock[T]` for non-generic and generic interfaces
- [Expectations DSL](expectations.md) — `.On` / `.Match` / `.OnAny` / `.Returns` / `.Times` and friends
- [Setup](setup.md) — IDE and terminal configuration
