# Design Document

## Problem

Go's strict static dispatch makes it hard to mock functions in tests without changing production code. The common approaches all have significant drawbacks:

- **Dependency injection (interfaces)**: Requires designing all code around interfaces. Adds boilerplate and indirection even when there's only one implementation. Infects the entire call chain.
- **Function pointer variables**: Replace `func Greet(...)` with `var Greet = func(...)`. Works, but changes the function's nature — different godoc, can be accidentally reassigned. Pollutes production code with test concerns.
- **Runtime binary patching (gomonkey)**: Overwrites machine code at runtime with JMP instructions. Architecture-dependent, requires disabling inlining (`-gcflags=all=-l`), breaks on macOS Apple Silicon with hardened runtime, fragile across Go versions.

The goal: mock any function during tests with **zero changes to production code**, in a way that works with standard Go tooling and IDEs.

## Inspiration

Erlang's [meck](https://github.com/eproxus/meck) library provides exactly this experience on the BEAM VM — you can replace any module's functions during tests and restore them after. BEAM supports hot code loading; Go doesn't, so we need a compile-time approach.

## Chosen approach: toolexec + AST rewriting

### How it works

1. The toolexec **pre-scans** all `_test.go` files in the module for `rewire.Func(t, pkg.Func, ...)` calls
2. It builds a targeted mock list: which package + function combinations need rewriting
3. When `rewire` intercepts a `compile` invocation for a package with targets, it:
   - Rewrites only the targeted functions (adds `Mock_` variable + nil-check wrapper)
   - Writes rewritten source to temp files
   - Invokes the real compiler with the temp file paths
4. When compiling a **test package** (has `_test.go` files), rewire also:
   - Generates a `_rewire_init_test.go` file with `init()` that registers mock var pointers
   - Registration is built directly from mock targets — no manifest files needed
5. For all other tool invocations (link, asm, packages without targets), rewire passes through unchanged

### The rewrite transformation

```go
// Input (production source, never modified on disk):
func Greet(name string) string {
    return fmt.Sprintf("Hello, %s!", name)
}

// Output (only exists during compilation):
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

The wrapper uses `_rewire_mock` as the local variable name to avoid shadowing function parameters (e.g., math functions commonly use `f` for `float64`).

### The registration mechanism

For each test binary, toolexec generates a registration file directly from mock targets:

```go
package foo

import (
    "github.com/GiGurra/rewire/pkg/rewire"
    _rewire_bar "github.com/example/bar"
    _rewire_math "math"
)

func init() {
    rewire.Register("github.com/example/bar.Greet", &_rewire_bar.Mock_Greet)
    rewire.Register("math.Pow", &_rewire_math.Mock_Pow)
}
```

Import aliases (`_rewire_bar`, `_rewire_math`) avoid conflicts with user imports.

At test time, `rewire.Func(t, bar.Greet, replacement)`:
1. Calls `runtime.FuncForPC(reflect.ValueOf(bar.Greet).Pointer())` to get the function name
2. Looks up the mock var pointer in the registry
3. Uses `reflect` to set the mock var to the replacement
4. Registers a `t.Cleanup` to restore the original value

### Targeted rewriting (pre-scan)

Earlier iterations rewrote ALL exported functions in same-module packages. This was simple but had problems:
- Slow for large packages (stdlib packages have many exported functions)
- Compiler directives (`//go:nosplit`, etc.) got displaced during AST reformatting
- Variable shadowing (wrapper's `f` conflicting with function parameters)
- Unnecessary — most functions are never mocked

The current approach pre-scans test files to build a precise target list. Only functions explicitly referenced in `rewire.Func` calls are rewritten. This solves all the above issues and enables external package mocking (stdlib, third-party).

The chicken-and-egg problem (dependencies compile before test packages) is solved by scanning test files upfront: the toolexec walks the module directory for `_test.go` files, parses them with `go/ast`, and extracts `rewire.Func` call targets. Results are cached per build session.

### Compiler intrinsics

Some functions (e.g., `math.Abs`, `math.Sqrt`, `math.Floor`) are replaced by CPU instructions at the **call site** by the Go compiler. Even though the source file is compiled and our wrapper exists, callers bypass it entirely — the compiler emits hardware instructions like `FABS` instead of a function call.

Rewire detects these by parsing `$GOROOT/src/cmd/compile/internal/ssagen/intrinsics.go` (the compiler's own intrinsic registry). If a user tries to mock an intrinsic, the build fails with:

```
rewire: error: function math.Abs cannot be mocked.
  It is a compiler intrinsic — the Go compiler replaces calls to it
  with a CPU instruction, bypassing any mock wrapper.
```

### Build cache strategy

Go's build cache keys on compilation inputs including the toolexec binary. Two recommended setups:

**Separate test cache (recommended):**
```bash
GOFLAGS="-toolexec=rewire" GOCACHE="$HOME/.cache/rewire-test" go test ./...
```

Tests use their own cache. `go build` uses the default cache without toolexec. No cross-contamination.

**Global GOFLAGS (simpler, minimal overhead):**
```bash
export GOFLAGS="-toolexec=rewire"
```

Both `go build` and `go test` use toolexec. Since only targeted functions are rewritten (not all exported functions), the production overhead is a single nil check per mocked function — negligible.

### Test isolation

Go compiles a separate test binary for each package:
- Package `foo`'s tests mock `bar.Greet` — only foo's binary has the mock registered
- Package `baz`'s tests use real `bar.Greet` — the mock var exists but is nil
- No configuration needed to scope mocks per test package

Within a test package, `rewire.Func` uses `t.Cleanup` to restore the mock variable after each test.

## Approaches considered and rejected

### Runtime binary patching (gomonkey-style)

**What it is**: Overwrite function machine code at runtime with a JMP instruction.

**Why rejected**: Architecture-dependent, requires `-gcflags=all=-l` to disable ALL inlining, breaks on macOS Apple Silicon (W^X enforcement), fragile across Go versions, blocked in some CI environments.

**What it's better at**: Zero setup, works on any function including third-party and intrinsics.

### Build tags with separate mock files

**What it is**: Keep original in `bar.go` with `//go:build !rewire`, generate mock in `bar_rewire.go` with `//go:build rewire`.

**Why rejected**: IDE needs build tag configured, puts generated files alongside production code, build tags affect all compilation not just tests.

### -overlay flag

**What it is**: Go's `-overlay=overlay.json` substitutes source files during compilation.

**Why rejected for v1**: Requires a daemon or pre-build step, complex cache invalidation. Interesting for v2 because `-overlay` is respected by `gopls`, enabling IDE integration.

### go:linkname for cross-package variable access

**What it is**: Use `//go:linkname` to access a shared mock registry without imports.

**Why rejected**: Go 1.23+ restricts `go:linkname`, requires `import _ "unsafe"`, fragile across versions.

### Source-level function pointer vars

**What it is**: Replace `func Greet(...)` with `var Greet = func(...)`.

**Why rejected**: Changes the declaration nature, can be accidentally reassigned, different `go doc`, pollutes production code. The explicit requirement was zero production code changes.

### Blanket rewriting of all exported functions

**What it was**: Earlier iteration rewrote ALL exported functions in same-module packages.

**Why replaced**: Slow for large packages, broke stdlib packages (compiler directives, variable shadowing), prevented external package mocking. Replaced by targeted rewriting based on pre-scanning test files.

## Interface mock generation

In addition to compile-time function rewriting, rewire also generates mock structs for Go interfaces via `rewire mock`. This is standard code generation (not toolexec) — the generated files are committed to the repo and work with `go:generate`.

This covers the "dependency injection" side of mocking: when you have an interface and want to pass in a mock implementation. Combined with `rewire.Func` for compile-time function/method replacement, rewire is a complete mocking solution — no need for a second mocking library.

### Design choices

- **Codegen over toolexec**: Interface mocks are generated files, not compile-time artifacts. This gives full IDE support (gopls sees the types), reviewable diffs, and follows standard Go patterns (`go:generate`).
- **Function fields**: Each method becomes a function field (`GetFunc`, `SetFunc`). Unset fields return zero values via named return parameters and bare `return`.
- **Only direct methods**: Embedded interfaces are not resolved — only methods directly declared on the interface are generated. This keeps the generator simple and avoids cross-package type resolution.

## Implemented features

### Method support

Methods use `(*Type).Method` or `Type.Method` syntax, matching Go method expressions and `runtime.FuncForPC` naming conventions. The rewriter generates a mock variable with the receiver as the first parameter (`Mock_Server_Handle func(*Server, string) string`), a wrapper method that forwards the receiver to the mock, and a `_real_` method that preserves the original body. The scanner detects both pointer receiver (`(*pkg.Type).Method`) and value receiver (`pkg.Type.Method`) patterns in test files.

Method mocks are global — all instances of a type share one mock variable. Per-instance mocking would require a map lookup on every call and a different API; for per-instance behavior, Go interfaces are the idiomatic approach.

## Future work

### gopls integration via -overlay

Generate an overlay JSON file mapping source files to rewritten versions. `gopls` would see mock variables and provide autocomplete. A `rewire daemon` could keep the overlay in sync.

### Generic function support

Generic functions need mock variables with matching type parameters.

### Parallel test safety

Parallel tests that mock the same function race on the mock variable. Options: goroutine-local storage (not native in Go), per-test context threading, or documenting the limitation.
