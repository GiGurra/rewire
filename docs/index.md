# Rewire

[![CI Status](https://github.com/GiGurra/rewire/actions/workflows/ci.yml/badge.svg)](https://github.com/GiGurra/rewire/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/GiGurra/rewire)](https://goreportcard.com/report/github.com/GiGurra/rewire)

A compile-time mocking toolkit for Go. Mock anything — free functions, stdlib, third-party, methods, specific instances, interfaces — with one small API, no interface extraction, no dependency-injection plumbing, no `go generate` code generation, no runtime patching.

```go
// Mock a stdlib function:
rewire.Func(t, os.Getwd, func() (string, error) { return "/mocked", nil })

// Mock a struct method, globally:
rewire.Func(t, (*bar.Server).Handle, func(s *bar.Server, req string) string { return "mocked" })

// Mock a struct method, for one specific receiver only:
rewire.InstanceMethod(t, s1, (*bar.Server).Handle, func(s *bar.Server, req string) string { return "s1-only" })

// Create a mock of an interface with zero committed files:
greeter := rewire.NewMock[bar.GreeterIface](t)
rewire.InstanceMethod(t, greeter, bar.GreeterIface.Greet, func(g bar.GreeterIface, name string) string { return "hi" })

// Multi-pattern stubs with call-count verification:
e := expect.For(t, bar.Greet)
e.On("Alice").Returns("hi Alice").Times(1)
e.OnAny().Returns("hi other")
```

One package, one API surface. Every scenario above uses the same underlying mechanism: rewire intercepts the Go compiler via `-toolexec` and rewrites or synthesizes code in-memory during compilation. Your source on disk is never modified. Nothing is patched at runtime. No `unsafe`, no platform-specific code, no inline-breaking tricks.

## How is this different?

Most Go mocking libraries pick *one* mocking style and stick to it — either interface-based dependency injection, or code generation via `go:generate`, or runtime binary patching. Each has well-known trade-offs, and switching between them mid-project typically means switching libraries.

Rewire's bet is that the same compile-time rewriting machinery can cover all the common cases with a single small API:

- **Function-level mocks** — mock `math.Pow`, `http.Get`, or any function in any package, without an adapter layer. Unlike runtime binary patching, rewire's approach is portable, plays nicely with inlining, and doesn't require `unsafe`.
- **Method mocks without interfaces** — mock `(*Server).Handle` globally using a Go method expression. No interface extraction, no dependency injection plumbed through the call chain.
- **Per-instance method mocks** — `rewire.InstanceMethod` scopes a method replacement to one specific receiver. Other instances run the real method body. Traditionally this requires an interface; rewire does it via compile-time dispatch tables.
- **Interface mocks with no committed files** — `rewire.NewMock[T]` synthesizes a backing struct at compile time, triggered purely by a reference in a test. No `go:generate`, no `mock_*_test.go` files in your repo.
- **A fluent expectation DSL** — `expect.For` / `expect.ForInstance` layer multi-pattern stubs, argument predicates, call-count bounds, and async synchronization on top of every other mocking style above. One DSL across all of them.

All of these compose in a single test. You can mix a function mock, a global method mock, a per-instance method mock, and an interface mock — and they all share one API vocabulary.

## Quick example

A fully self-contained test — just stdlib, no production code to set up. We mock `os.Getwd` and then call `filepath.Abs`, which internally uses `os.Getwd` to resolve a relative path:

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

`filepath.Abs` lives in `path/filepath`, it calls `os.Getwd` which lives in `os`, and neither package belongs to your project. Rewire rewrites `os.Getwd` at compile time so when `filepath.Abs` reaches the call site, it gets the mocked version. No interfaces, no dependency injection, no wrappers — and the mock is automatically restored after the test.

## About this project

This project is 100% vibe coded — AST rewriting and compiler toolchains are way outside my comfort zone. Built entirely with [Claude Code](https://claude.ai).

Inspired by Erlang's [meck](https://github.com/eproxus/meck). The user experience is similar; the underlying mechanism is entirely different (compile-time AST rewriting vs. runtime hot-patching).

## Next steps

- [Getting Started](getting-started.md) — install and run your first mock
- [Function Mocking](function-mocking.md) — replace any function at test time
- [Method Mocking](method-mocking.md) — global and per-instance method mocks
- [Interface Mocks](interface-mocks.md) — `rewire.NewMock[T]` for non-generic and generic interfaces
- [Expectations DSL](expectations.md) — declarative `.On(args).Returns(v).Times(n)` style, plus per-instance and interface-mock entry points
- [How It Works](how-it-works.md) — the toolexec pipeline, rewrite transformations, dispatch mechanics
- [Setup](setup.md) — IDE and terminal configuration
- [Limitations](limitations.md) — compiler intrinsics, parallel safety, and other edge cases
- [Roadmap](roadmap.md) — shipped features and what's next
