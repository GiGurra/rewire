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

## Interface mock generation: `rewire.NewMock[T]`

The scanner detects `rewire.NewMock[X]` references in test files, the codegen locates `X`'s source at compile time, and a concrete backing struct is synthesized into the test package's compile args — never written to disk, never committed.

Design choices:

- **No committed mock files.** Users reference the interface in a test; rewire handles the rest. Trade-off: gopls can't see the generated struct, so users never name it directly. The API is designed around that (`rewire.NewMock[I]` for creation, interface method expressions like `I.M` for stubbing), and in practice the generated type is invisible.
- **Same dispatch as per-instance method mocks.** Each generated method body consults a per-method `_ByInstance sync.Map` and falls back to zero-value returns. The sync.Map is populated by `rewire.InstanceMethod(t, mock, I.Method, replacement)` — one stubbing verb across concrete per-instance mocks and interface-method mocks.
- **Factory registry.** The generated file's `init()` registers a `func() any` factory keyed on the interface's fully-qualified name. `rewire.NewMock[I](t)` looks up the factory by `reflect.TypeFor[I]()`-derived key, calls it, and type-asserts back to `I`. Non-reflective at the hot path — factory lookup is O(1), struct construction is a plain `new`.
- **Non-zero-size backing struct.** Go's spec explicitly permits pointers to distinct zero-size variables to compare equal, which would break per-instance dispatch since the sync.Map keys on receiver pointer identity. The generator emits `struct{ _ [1]byte }` to force distinct allocations to get distinct addresses. Load-bearing, documented in the generator source.
- **Current scope.** Non-generic interfaces, generic interfaces with arbitrary type-argument shapes (builtins, slices, maps, pointers, external-package types, nested generic instantiations), embedded interfaces (same-file, same-package, cross-package — including generic embeds where the outer interface's type parameter flows into the embed), methods using imported types in their signatures, methods referencing bare same-package types (auto-qualified with the declaring package alias via an AST walker that runs BEFORE substitution, using the interface's type parameter names as a skip set), and interfaces declared in files with dot imports (`import . "pkg"` — the walker detects the dot import, asks a `PackageTypeLister` callback for the dot-imported package's exported types, and gives those names priority over same-package qualification). Each generic instantiation produces its own backing struct keyed on `reflect.TypeFor[I]()`, and the scanner forwards per-instantiation import resolution from the test file so type args from packages the interface's declaring file doesn't import (e.g. `Container[time.Duration]`) work correctly. Embedded-interface walking uses an `InterfaceResolver` callback the toolexec wrapper injects; mockgen itself does no filesystem I/O.
- **Module-aware package resolution.** Interface source lookup delegates to `go list -find -f '{{.Dir}}' <importpath>` (with `GOFLAGS` stripped so the subprocess doesn't recursively fire the toolexec). That gives rewire identical resolution semantics to the surrounding Go build system — `replace` directives in `go.mod`, workspace files (`go.work`), vendor directories, and module download locations all work out of the box. Stdlib packages take a fast path through `go/build.Default.Import` to skip the subprocess. Results are memoized per toolexec invocation.

A standalone `rewire mock` CLI subcommand that wrote committed mock files used to coexist with the toolexec path. It was removed once the toolexec path covered the common ground; the toolexec route is now the sole interface-mocking entry point in rewire.

## Implemented features

### Method support

Methods use `(*Type).Method` or `Type.Method` syntax, matching Go method expressions and `runtime.FuncForPC` naming conventions. The rewriter generates a mock variable with the receiver as the first parameter (`Mock_Server_Handle func(*Server, string) string`), a wrapper method that forwards the receiver to the mock, and a `_real_` method that preserves the original body. The scanner detects both pointer receiver (`(*pkg.Type).Method`) and value receiver (`pkg.Type.Method`) patterns in test files.

Method mocks set via `rewire.Func` are global — all instances of the type share one package-level mock variable. For per-instance scoping, `rewire.InstanceMethod` emits an additional `Mock_Type_Method_ByInstance sync.Map` and a per-instance dispatch ahead of the global mock in the wrapper body. The extra emission is opt-in at compile time: the scanner only triggers it when it finds `rewire.InstanceMethod` / `rewire.RestoreInstanceMethod` calls referencing the target, so tests that only use `rewire.Func` pay no per-call cost.

## Future work

### gopls integration via `-overlay`

Generate an overlay JSON file mapping source files to rewritten versions. `gopls` would see mock variables (`Mock_Greet`, `Real_Greet`) and provide autocomplete inside test code. A `rewire daemon` could keep the overlay in sync. Not started — the current experience (gopls resolves `rewire.Func(t, bar.Greet, ...)` as an ordinary function call, so no IDE friction on the happy path) has been good enough so far.

### Interface mock Phase 2 (remaining work)

Generic interfaces (Phase 2a), same-package bare type qualification (Phase 2b), embedded interfaces (Phase 2c), module-aware package resolution (Phase 2d, via `go list -find`), and dot-import support (Phase 2e) are all shipped — see the `Current scope` note above. No further milestones are planned for the interface-mock generator at this time.

### Generic interfaces (shipped)

`rewire.NewMock[Container[int]]` with per-instantiation dispatch. The runtime composite key is derived from `reflect.TypeFor[I]()` so each instantiation produces its own factory entry, and the generator performs AST-level type-parameter substitution over the interface's method signatures. Type args can be arbitrary Go type expressions including builtins, slices, maps, pointers, external-package types, and nested generic instantiations. The scanner forwards per-instantiation import resolution from the test file so external-package type args (`Container[time.Duration]`) emit the right imports in the generated mock file.

### API consolidation and rename pass

Once the full feature scope has settled, a coherent cleanup of the public API surface — collapsing `Func` / `InstanceMethod` / `Restore`'s overloaded semantics into a more uniform verb set, rethinking whether the `expect` package splits into `For` / `ForInstance` or unifies under an options pattern. Deliberately deferred to avoid piecemeal renames. See `plans/TODO_next_session.md`.

### Parallel test safety

Parallel tests that mock the same function race on the shared `Mock_` variable. Options considered: goroutine-local storage (not native in Go), per-test context threading (intrusive), or continuing to document the limitation. Not currently planned — most users hit this only when trying to shove two instance-scoped mocks into parallel tests, and those cases are already handled by `rewire.InstanceMethod` + per-receiver keying.

## Why toolexec, and what we'd consider if it becomes insufficient

toolexec is the blessed compiler-extension point in Go — stable API since Go 1.7, hooks directly into `go build` / `go test`, and lets us see exactly the source files the compiler sees. Every rewire feature we've built so far expresses cleanly as a source-level transformation, and source is the right abstraction for what we do: our rewrites survive inlining, cross-package boundaries, and Go version churn. We've deliberately avoided patterns that would require any lower-level access. This section records the alternatives we'd consider if toolexec ever stops being enough, and explains why we don't use them today.

### Post-processing compiled output

**What it is**: Let `compile` produce `.a` archive files normally, then modify the compiled object code.

**Why not**: Binary patching. Modifying `.a` archives is platform-specific disassembly + symbol-table surgery, and you can't add new package-level variables (like our `Mock_Foo` symbols) because the compiler decided the object layout before you got the file. This is how runtime monkey-patching libraries work and it's notoriously fragile across Go versions, architectures, and build modes. We'd be trading a clean source rewrite for a dangerous binary rewrite.

### A custom Go compiler fork

**What it is**: Fork `cmd/compile`, add hooks for rewrite/injection passes, ship our own Go toolchain.

**Why not**: Maintenance cost is enormous — Go releases twice a year and refactors compiler internals aggressively with no stability guarantees. User experience collapses too ("install our fork of Go" is a non-starter). And it buys us nothing: our modifications are all expressible in source code, so operating at source level is actually cleaner than IR/SSA, because source survives inlining decisions and cross-package boundaries in ways IR doesn't.

### `-overlay` flag integration

**What it is**: Go's `-overlay=<json>` flag maps original file paths to replacement content. Introduced for `gopls`, respected by `go build` / `go test` / the whole `go/packages` stack.

**Why we don't use it today**: The overlay is a flat global map, but rewire's rewrites are per-compile-step variable — each test package wants its own generated registration file based on which `rewire.Func` / `rewire.NewMock` calls appear in *that* package's test files. A single global overlay can't express per-package variability.

**Why it might come back**: If we ever want gopls to see our rewritten sources (so IDE autocomplete on `Mock_Foo` variables "just works"), overlay is the obvious mechanism. It doesn't have to replace toolexec — the two could complement each other, with toolexec handling per-compile variability and overlay handling the stable parts gopls cares about.

### Build-system integration (Bazel, Please, Pants)

**What it is**: Integrate at the build-system level rather than the compiler level. These tools have better caching than plain `go build`.

**Why not**: Excludes plain-`go build` users, which is the vast majority. rewire is meant to be drop-in for any Go project; adopting it shouldn't require adopting Bazel.

### Pre-build staged copy via `packages.Load`

**What it is**: Skip the compiler integration entirely. `rewire build ./...` runs `packages.Load`, applies all rewrites to a staged copy of the module, then invokes `go test` against that copy. Some Go tools work this way (e.g. `go-mutesting`).

**Why not**: Cleaner conceptual model, but duplicates the build tree, loses Go's incremental compile caching, and puts more memory pressure on large projects. Probably worse than toolexec in practice. The one scenario where it might win is if we needed `go/types`-level resolution for every package, which we don't.

### Bottom line

toolexec gives us everything we need without forcing users to adopt anything exotic. Future effort should go into making toolexec usage *faster* — caching, parallelism, smaller scan surface — rather than trying to get below it. See [Performance](#performance) below for the current numbers and how they were measured.

## Performance

Rewire inserts itself into `go build` / `go test` via `-toolexec`, so every compile invocation pays some overhead: scanning test files once per build, rewriting targeted source files, synthesizing interface mock backing structs, patching the importcfg for new stdlib imports, etc. The interesting questions are: how much does this cost in absolute terms, and does it scale linearly with module size (or super-linearly, which would be a bug)?

### Harness

Rewire ships a Go benchmark harness at `scripts/benchtool/` with three subcommands:

- `benchtool gen -n <N> -o <dir>` — generate a synthetic Go module with `N` packages at 10% mock density (every 10th package contains `rewire.Func` + `rewire.NewMock` calls, the rest are plain).
- `benchtool bench -target <dir>` — time `go test -run ^$ -count=1 <pkgs>` in two modes (baseline without toolexec, rewire with `-toolexec=rewire`) against the target module. Pre-warms caches, discards iteration-1 outliers via the pre-warm, computes mean/stddev/min/max over `-iters` samples, reports overhead ratio.
- `benchtool scale -sizes 10,25,50,100` — runs `gen` and `bench` across a range of module sizes to measure scaling. Writes each completed row to stdout as it finishes (no tail-buffering trap) and optionally to an `-incremental` JSON Lines file so killing mid-run keeps partial data.

What we measure is **compile time**, not test runtime: `-run ^$` matches no tests so nothing executes, but the full compile + link pipeline runs (which is where rewire's toolexec does its work). That's the right measurement because test runtimes don't change between modes, so including them just adds noise, and tests that rely on the toolexec at runtime (e.g. `NewMock` factories) can't pass in baseline mode at all.

### Scaling result at 10% mock density

Cold-compile benchmarks against synthetic modules with **10% mock density** — every 10th package contains `rewire.Func` + `rewire.NewMock` calls, the rest are plain Go. Apple Silicon, Go 1.26.2, 3 timed iterations per size after a pre-warm run:

| N packages | baseline (s)   | rewire (s)     | ratio | overhead % | overhead/pkg |
|-----------:|---------------:|---------------:|------:|-----------:|-------------:|
|         10 | 3.462 ± 0.056  | 4.848 ± 0.091  | 1.40× |     +40.0% |       139 ms |
|         25 | 5.221 ± 0.034  | 8.344 ± 0.047  | 1.60× |     +59.8% |       125 ms |
|         50 | 8.176 ± 0.054  | 14.336 ± 0.155 | 1.75× |     +75.3% |       123 ms |
|        100 | 13.987 ± 0.125 | 25.515 ± 0.239 | 1.82× |     +82.4% |       115 ms |

(Your hardware may differ; run `scripts/benchtool scale` on your own machine for local numbers.)

**Per-package overhead decreases as N grows** (139 ms → 115 ms). That's the signature of a linear-plus-fixed cost model, not super-linear scaling. A least-squares fit across these samples (R² ≈ 1.00):

```
baseline(N) = 2.30 s (fixed) + 0.117 s × N
rewire(N)   = 2.64 s (fixed) + 0.230 s × N
overhead(N) = 0.34 s (fixed) + 0.113 s × N
```

The ratio asymptotes at **~1.96× as N → ∞**: rewire approaches but never quite reaches 2× baseline in this worst-case density.

### Why does per-pkg overhead decrease while the ratio increases?

This looks contradictory but isn't — they measure different things. The ratio climbs with N *because* baseline's own fixed cost (≈2.3 s of toolchain startup + test-harness linking) gets diluted at large N, so the ratio-denominator drops relative to its asymptotic behavior. The per-pkg overhead drops with N *because* rewire's own fixed cost (≈0.34 s scanner + one-time setup) gets amortized over more packages. A single linear model predicts both:

- At N=10: baseline is 66% fixed cost — rewire's per-pkg work looks small relative to total. Ratio 1.40×.
- At N=100: baseline is 84% per-pkg — the ratio now reflects the true per-pkg cost of rewire vs the compiler. Ratio 1.82×, approaching 1.96×.

Both numbers are consistent with the same linear fit. A super-linear term would make per-pkg overhead *increase* with N, which is the opposite of what we see.

### Where the overhead actually comes from

Bisecting rewire's per-invocation work reveals an important property: **almost none of the measurable overhead is in rewire's own code**. A level-by-level measurement at N=25:

| Short-circuit point                                    | Wall time |
|--------------------------------------------------------|----------:|
| Skip everything — immediate `syscall.Exec`             |    5.22 s |
| + findModuleInfo                                        |    5.28 s |
| + scan-cache read                                       |    5.34 s |
| + target/byInstance/isTest lookups                      |    5.40 s |
| Full rewire work, **drop rewritten args**, use originals |    5.27 s |
| Full rewire work, use rewritten args (syscall.Exec)    |    8.15 s |
| **Full flow**                                          |    8.25 s |

The jump from "drop rewritten args" (5.27 s) to "use rewritten args" (8.15 s) is nearly the entire overhead. Rewire's own work — scanning, rewriting, generating interface mock files, patching importcfg — costs essentially nothing. The overhead is in **the Go compiler processing the extra source code that rewire adds to test compilations**.

Each mocked interface produces a ~35-line backing-struct file (type declarations + per-method `sync.Map`-backed dispatch + `init()` that registers the factory). Each test package that references `rewire.Func` produces a ~15-line registration file. For the synthetic N=25 benchmark with 3 mock-heavy test packages, that's ~150 lines of extra Go per compile, compiled once per affected test binary. The Go compiler has a real per-file fixed cost for parse + type-check + codegen, and it adds up.

### This cost is not unique to rewire

**Any mocking library that produces Go code pays this exact compile cost.** `mockery`, `gomock`, `moq`, `counterfeiter` — they all generate backing structs with method implementations. The only difference is *where* the cost lands:

- **Code-generator tools commit their output to disk.** The generated mock files live alongside your source, and their compile cost is folded into your "normal" build time. It's invisible because it's always there.
- **Rewire generates on the fly** during the build. The same cost shows up as a delta between `go test` and `go test -toolexec=rewire`, which makes it look like "rewire overhead" when it's really "the cost of compiling mock code."

A fair comparison isn't "rewire vs no-rewire" — it's **"any mocking library at this mock density vs a codebase with no mocks at all."** Under that framing, rewire's ~0.1 s per mocked test package is the structural cost of the feature, not an implementation inefficiency.

The upside of rewire's on-the-fly approach is that the generated code **doesn't clutter your repo** — no `mock_*.go` files to commit, review, or keep in sync when interfaces change. The downside is that the cost is visible in `go test` wall-clock time the first time you measure it. Both approaches compile the same amount of Go.

### Typical-density projects

10% mock density is fairly representative of a test-heavy Go codebase: if most test packages use mocks, 10% of the module's compile work is mock-related. Real projects vary:

- **Mock-heavy**: 20-40% of packages have mock-using tests. Ratio in the 1.6×-2.0× range (mostly the fundamental mock-compile cost described above).
- **Typical**: 5-15% of packages. Ratio 1.3×-1.6×.
- **Sparse**: <5%. Ratio <1.3×, sometimes barely measurable.

All of these are for **cold-cache** compiles. Incremental rebuilds (the normal TDD loop) bypass toolexec entirely for cached packages, so the overhead is **essentially zero** — see the warm-cache section below. The ratio only matters on the first full rebuild after a `go clean -cache`.
```

Plugging into the ratio formula, the asymptotic ratio drops from ~1.96× (dense) to:

```
(0.117 + 0.038) / 0.117  ≈  1.33×
```

So a typical real-world project should see rewire asymptote somewhere around **1.3×-1.4×**, not 2×. The 2× is specifically the "every tenth package is mock-heavy" pessimistic case, and even that is measured cold-cache — incremental rebuilds (where most packages are cached) see **effectively zero overhead**.

### Warm-cache / incremental is free

When Go's build cache is warm and only a handful of packages need recompilation, rewire's toolexec doesn't even fire for the cached packages. Running the example suite's bench with `-warm` mode (pre-warm then measure with the build cache populated):

```
baseline: 0.385 s ± 0.012 s
rewire:   0.392 s ± 0.004 s
overhead: +0.007 s  +1.8%  (1.02×)
```

The ~2% difference is within measurement noise. Incremental TDD workflows feel no rewire overhead at all.

### What the overhead is spent on

The `REWIRE_PROFILE=1` env var enables per-stage wall-clock logging in the toolexec wrapper. Each stage emits a line like:

```
rewire-profile stage=scan duration_ms=42.31 pid=91508
rewire-profile stage=resolve-pkg-dir pkg=github.com/example/bar duration_ms=31.88 pid=91508
rewire-profile stage=rewrite-compile-args pkg=example/foo duration_ms=12.44 pid=91508
```

Instrumented stages: `scan` (test-file scanner pass, cached on disk per build), `compile-wrap` (per-compile wrapper lifetime), `rewrite-compile-args` (source rewriting), `resolve-pkg-dir` (per-package `go list` subprocess), `iface-mock-gen` (interface backing-struct synthesis). Zero overhead when disabled — the stage function bails on a single atomic load.

### Caches and amortization

Rewire carries a few caches that keep the toolexec wrapper's own cost well below noise:

- **Scan cache.** `scanAllTestFiles` walks every `_test.go` in the module and parses each once. The result is cached on disk at `$TMPDIR/rewire-<ppid>/mock_targets.json`. Reads are lock-free (the cache file is written atomically via temp + rename), so parallel toolexec processes don't serialize on it. Only the first toolexec invocation in a build pays the walk+parse cost; every subsequent invocation reads the JSON directly and moves on.
- **Intrinsic function table.** The compiler's `intrinsics.go` is parsed once per toolexec process via `sync.Once`. Without this cache, a package with N mocked functions would re-read and re-regex the intrinsics file N times per compile invocation.
- **Package directory cache.** `resolvePackageDir` memoizes `go list -find` results in an in-process `sync.Map`, so a compile step that mocks several interfaces from the same package shells out once per package path, not once per interface.
- **No-op fast path.** Compile invocations for packages that don't need any rewire work (non-compile tools, missing `-p` flag, no module context, no mocked functions and not a test binary) use `syscall.Exec` to replace the rewire process with the real compiler directly — no fork+wait, no parent process waiting for a child. On macOS this saves roughly 1 ms per invocation; on Linux the savings are similar.

These optimizations collectively keep the per-invocation wrapper cost under 1 ms, which is why the bisection table above shows essentially no gap between "full rewire work" and "baseline" — rewire's own code is not the bottleneck. The compile cost of the generated mock source *is*, and it's fundamental to having mocks at all.
