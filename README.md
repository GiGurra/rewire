# rewire

[![CI Status](https://github.com/GiGurra/rewire/actions/workflows/ci.yml/badge.svg)](https://github.com/GiGurra/rewire/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/GiGurra/rewire)](https://goreportcard.com/report/github.com/GiGurra/rewire)
[![Docs](https://img.shields.io/badge/docs-gigurra.github.io%2Frewire-blue)](https://gigurra.github.io/rewire/)

> **Experimental** — this project is in early development. Both the implementation and APIs may change at any time. Use at your own risk.

Mock anything — No `go generate` code generation, no runtime patching.

```go
// Mock a function:
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

**rewire intercepts the Go compiler via `-toolexec` and rewrites or synthesizes temporary code during compilation.** Your source on disk is never modified. No `unsafe`, no platform-specific code, no inline-breaking tricks. Your IDE sees valid syntax.

## Quick start

```bash
# Install the rewire binary
go install github.com/GiGurra/rewire/cmd/rewire@latest

# Add the test library to your module
go get github.com/GiGurra/rewire/pkg/rewire

# Clean the Go build cache (needed once, so rewire can rewrite cached packages)
go clean -cache

# Run tests with rewire
GOFLAGS="-toolexec=rewire" go test ./...
```

See [Setup](#setup--ide-and-terminal-configuration) below for how to wire it into your IDE cleanly so `go build` and `go test` stay on separate caches.

<details>
<summary><strong>Function mocking</strong> — any function, including stdlib and third-party</summary>

Here's a fully self-contained example. We mock `os.Getwd` and then call `filepath.Abs`, which internally calls `os.Getwd` to resolve a relative path. Neither function is in your project, and you change nothing about them.

```go
func TestFilepathAbs_WithMockedOsGetwd(t *testing.T) {
    rewire.Func(t, os.Getwd, func() (string, error) {
        return "/mocked", nil
    })

    got, _ := filepath.Abs("foo")
    // got == "/mocked/foo"
}
```

The same API works on your own code, on third-party packages, on stdlib internals that aren't exposed as interfaces. Pass the original function and its replacement. No mock variable names, no generated types, no wrappers. Mocks auto-restore via `t.Cleanup`.

**Generic functions** — pass the instantiation you want to mock, and only that instantiation is replaced:

```go
rewire.Func(t, bar.Map[int, string], func(in []int, f func(int) string) []string {
    return []string{"mocked"}
})
// bar.Map[float64, bool] still runs the real body in the same test
```

**Spy pattern** — use `rewire.Real` to capture the pre-rewrite implementation and delegate to it from inside your mock:

```go
realGreet := rewire.Real(t, bar.Greet)

rewire.Func(t, bar.Greet, func(name string) string {
    return realGreet(name) + " [wrapped]"
})
```

**Mid-test cleanup** — `rewire.Restore(t, target)` ends a mock early if you want the real implementation back before the test finishes. Idempotent.

See [Function Mocking](docs/function-mocking.md) for the full feature set.

</details>

<details>
<summary><strong>Method mocking</strong> — global or per-instance, no interface required</summary>

### Global (all instances)

```go
func TestGreetWith_MockedMethod(t *testing.T) {
    rewire.Func(t, (*bar.Greeter).Greet, func(g *bar.Greeter, name string) string {
        return "Mocked, " + name
    })
    // Every *bar.Greeter.Greet call in this test returns the mock.
}
```

Both pointer (`(*Type).Method`) and value (`Type.Method`) receivers work. The replacement takes the receiver as its first argument.

### Per-instance

For tests where only one specific receiver should be mocked:

```go
s1 := &bar.Server{Name: "primary"}
s2 := &bar.Server{Name: "secondary"}

rewire.InstanceMethod(t, s1, (*bar.Server).Handle, func(s *bar.Server, req string) string {
    return "primary-mock: " + req
})

s1.Handle("ping") // "primary-mock: ping"  — per-instance mock
s2.Handle("ping") // real Handle body      — s2 is untouched
```

Dispatch order inside the wrapper is per-instance → global → real, so per-instance and global mocks compose. You can `rewire.Restore(t, s1)` to drop every per-instance mock bound to `s1` at once, or `rewire.RestoreInstanceMethod(t, s1, target)` for one specific entry.

Works for generic methods too:

```go
rewire.InstanceMethod(t, c1, (*bar.Container[int]).Add, func(c *bar.Container[int], v int) {
    // swallow — c1 never actually appends
})
```

See [Method Mocking](docs/method-mocking.md) for the full feature set.

</details>

<details>
<summary><strong>Interface mocks</strong> — <code>rewire.NewMock[T]</code>, no go:generate, no committed files</summary>

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

No `go:generate` step. No `mock_*_test.go` files committed to the repo. The toolexec wrapper scans test files for `rewire.NewMock[X]` references, parses the interface's source, and synthesizes a backing struct into the test package's compile args. The struct satisfies `X`, routes method calls through the same per-instance dispatch tables that back `rewire.InstanceMethod`, and disappears the moment the test binary finishes.

- Two mocks of the same interface are scoped independently — stubs on one don't leak to the other.
- Unstubbed methods return zero values.
- `rewire.Restore(t, mock)` clears every stub on a mock.

**Current scope:** non-generic *and* generic interfaces. Generic interfaces support arbitrary type arguments — builtins, slices, maps, pointers, types from external packages, even nested generic instantiations like `Container[Container[int]]` — and each instantiation produces its own backing struct keyed by reflect's instantiation-aware type name. Embedded interfaces and types from the interface's own declaring package are still rejected with clear errors (Phase 2b/2c).

Rewire also ships an older `rewire mock` CLI that writes a committed mock file — useful when you want to review the generated code. It's still fully supported; over time the `rewire.NewMock[T]` path is intended to become rewire's canonical interface-mock API. See [Interface Mocks](docs/interface-mocks.md) for both styles.

</details>

<details>
<summary><strong>Expectation DSL</strong> — multi-pattern stubs, call counts, predicates, async wait</summary>

For tests that need more than a single closure — multiple rules, argument predicates, call-count verification — the `expect` package layers a fluent DSL on top of any rewire mock:

```go
import "github.com/GiGurra/rewire/pkg/rewire/expect"

e := expect.For(t, bar.Greet)
e.On("Alice").Returns("hi Alice")
e.On("Bob").Returns("hi Bob")
e.Match(func(name string) bool { return strings.HasPrefix(name, "admin_") }).Returns("admin")
e.OnAny().Returns("hi other")
```

- **First-fit matching** — rules are walked in declaration order, first match wins.
- **Typed predicates** — `.Match(func(...))` takes a real Go function, fully type-checked.
- **Call-count bounds** — `.Times(n)`, `.AtLeast(n)`, `.Never()`, `.Maybe()`. The default for `.On` and `.Match` is `.AtLeast(1)` — so "was this mock actually called?" verification is free.
- **Async support** — `.Wait(n, timeout)` blocks until the rule has matched n times, for tests involving background goroutines.
- **Spy-friendly** — `.DoFunc(func(...))` runs arbitrary code on each call, useful for capturing arguments or delegating to the real implementation.

The same DSL works on per-instance and interface mocks via `expect.ForInstance(t, instance, target)`:

```go
greeter := rewire.NewMock[bar.GreeterIface](t)

e := expect.ForInstance(t, greeter, bar.GreeterIface.Greet)
e.On(greeter, "Alice").Returns("hi Alice")
e.OnAny().Returns("hi other")
```

One rule-builder API — `.On` / `.Match` / `.OnAny` / `.Returns` / `.DoFunc` / `.Times` / `.AtLeast` / `.Never` / `.Maybe` / `.Wait` — spans free functions, concrete methods (global), concrete methods (per-instance), and interface methods on `NewMock` instances.

See [Expectation DSL](docs/expectations.md) for the full reference.

</details>

<details>
<summary><strong>Why these capabilities might be unusual</strong></summary>

Go has plenty of mocking libraries, but most of them pick *one* spot on this grid and stay there. The combination rewire covers is unusual — possibly novel, depending on how you count. Here's a calibrated comparison without naming specific libraries:

| Capability | Typical Go approach | rewire |
|---|---|---|
| Mock a stdlib or third-party function | Runtime binary patching (unsafe, platform-specific, breaks under inlining) | Compile-time rewrite (safe, portable, verified compatible with inlining) |
| Mock a struct method without touching production code | Extract an interface + dependency injection | Method expression — no interface, no DI |
| Mock *one specific instance* of a type | Extract an interface, inject per instance | `rewire.InstanceMethod` — scoped by receiver pointer |
| Mock an interface | `go:generate` a mock file, commit it, regenerate on every change | `rewire.NewMock[T]` — backing struct synthesized at compile time, nothing committed |
| Multi-pattern expectations with call-count verification | A separate DSL tied to one of the above styles | `expect.For` / `expect.ForInstance` — the same DSL works on *all* of the above |

Each cell of rewire's column uses the same compile-time rewriting machinery underneath, so they compose. You can mix global mocks, per-instance mocks, and interface mocks in a single test, all with the same verbs.

"Might be" rather than "is" because the Go ecosystem is large and we haven't done a systematic survey — if a library already covers this combination and we missed it, please open an issue and tell us about it.

</details>

<details>
<summary><strong>How it works</strong> — compile-time rewriting via <code>-toolexec</code></summary>

1. **Pre-scan** — rewire walks `_test.go` files in your module and collects every `rewire.Func` / `rewire.InstanceMethod` / `rewire.NewMock[T]` reference. This builds a target list.
2. **Targeted rewrite** — when the Go compiler processes a package containing targeted functions or methods, rewire intercepts it via `-toolexec` and rewrites exactly those functions to route through a package-level mock variable and a nil-check wrapper. Everything else in the package compiles normally.
3. **Interface mock generation** — for each `rewire.NewMock[X]` target, rewire parses `X`'s source at compile time and synthesizes a concrete backing struct into the test package's compile args.
4. **Registration** — when compiling a test package, rewire generates an `init()` file that registers mock variable pointers (and per-instance dispatch tables, and mock factories) in a runtime registry.
5. **Runtime swap** — `rewire.Func` / `rewire.InstanceMethod` use `runtime.FuncForPC` to resolve the target name, look up the registry entry, and install the replacement via `reflect`. `t.Cleanup` restores the original.

Only functions and methods explicitly referenced in rewire calls are rewritten. Everything else passes through the compiler untouched — no whole-module overhead.

The rewrite transformation for a plain function (only exists during compilation):

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

Inlining is preserved — the wrapper is small enough to inline, and CI verifies this on every build.

</details>

<details>
<summary><strong>Setup</strong> — IDE and terminal configuration</summary>

### Recommended: test-specific cache

Keep test builds in a separate cache so `go build` and `go test` never interfere:

**Terminal (shell alias):**
```bash
alias gotest='GOFLAGS="-toolexec=rewire" GOCACHE="$HOME/.cache/rewire-test" go test'
```

**GoLand:** Run → Edit Configurations → Templates → Go Test → Environment variables:
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

With this setup `go build` uses the default cache (clean production binaries, no rewire artifacts) and `go test` uses a separate cache (rewire active, no conflicts).

**Cleaning the rewire cache:** `go clean -cache` wipes whichever cache `$GOCACHE` currently points at, so to clean the rewire-test cache specifically:

```bash
GOCACHE="$HOME/.cache/rewire-test" go clean -cache
```

### Alternative: global GOFLAGS

```bash
export GOFLAGS="-toolexec=rewire"
```

Simpler, but `go build` now also runs through the rewire toolexec. The overhead is tiny (a nil check per mocked function) but the split-cache setup is cleaner for day-to-day use.

</details>

<details>
<summary><strong>Limitations</strong></summary>

- **Compiler intrinsics** — `math.Abs`, `math.Sqrt`, `math.Floor` and friends are replaced by CPU instructions at the call site, bypassing any wrapper. Rewire detects these and fails with a clear error. Non-intrinsic alternatives (`math.Pow`, for example) work fine.
- **Bodyless functions** — functions implemented in assembly have no Go source to rewrite. Detected and rejected at compile time.
- **Parallel mocks on the same target** — two `t.Parallel()` tests that mock the same function with different replacements will race on the package-level mock variable. Rewire is single-test-at-a-time per target.
- **Interface mocks** — generic interfaces are supported (any type-argument shape, including nested generics and external-package type args). Embedded interfaces and types from the interface's own declaring package are still rejected with clear errors.

</details>

## Acknowledgements

100% vibe coded — AST rewriting and compiler toolchains are way outside my comfort zone. Built entirely with [Claude Code](https://claude.ai).

Inspired by Erlang's [meck](https://github.com/eproxus/meck). The user experience is similar; the underlying mechanism is entirely different (compile-time AST rewriting vs. runtime hot-patching).

## License

MIT
