# Rewire

**Compile-time function mocking and interface mock generation for Go**

[![CI Status](https://github.com/GiGurra/rewire/actions/workflows/ci.yml/badge.svg)](https://github.com/GiGurra/rewire/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/GiGurra/rewire)](https://goreportcard.com/report/github.com/GiGurra/rewire)

Rewire is a complete mocking solution for Go:

- **Replace any function or method at test time** — package-level functions, struct methods, stdlib, third-party. No interfaces, no dependency injection, no unsafe runtime patches. This is what other mocking libraries can't do.
- **Generate mock structs for interfaces** — for traditional dependency-injection style testing, like other Go mocking libraries.

One tool, both approaches. Production code on disk is never modified.

## How is this different?

Most Go mocking libraries require you to design your code around interfaces. If a function isn't behind an interface, you can't mock it. This means:

- You need to wrap stdlib calls in interfaces just for testing
- Third-party libraries need adapter layers
- Every call chain needs dependency injection plumbed through

**Rewire doesn't have these limitations.** It intercepts the Go compiler and rewrites function calls in-memory during compilation. You can mock `math.Pow`, `http.Get`, or any function in any package — without changing a single line of production code.

For cases where you *do* have interfaces (dependency injection), rewire also generates lightweight mock structs — so you don't need a second mocking library.

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

Notice what's happening: `filepath.Abs` lives in `path/filepath`, it calls `os.Getwd` which lives in `os`, and neither belongs to your project. Rewire rewrites `os.Getwd` at compile time, so when `filepath.Abs` reaches the call site, it gets the mocked version. No interfaces, no dependency injection, no wrappers — and the mock is automatically restored after the test.

## About this project

This project is 100% vibe coded — AST rewriting and compiler toolchains are way outside my comfort zone. Built entirely with [Claude Code](https://claude.ai).

Inspired by Erlang's [meck](https://github.com/eproxus/meck), although the underlying mechanism is entirely different.

## Next steps

- [Getting Started](getting-started.md) — install and run your first mock
- [Function Mocking](function-mocking.md) — replace any function at test time
- [Method Mocking](method-mocking.md) — mock struct methods
- [Expectations DSL](expectations.md) — declarative `.On(args).Returns(v).Times(n)` style mocking (opt-in)
- [Interface Mocks](interface-mocks.md) — generate mock structs for interfaces
- [Roadmap](roadmap.md) — planned work and known gaps vs other mocking libraries
