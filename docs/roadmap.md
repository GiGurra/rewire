# Roadmap

This page tracks rewire's shipped features, what's actively planned, and gaps we're deliberately not tackling. "Shipped" is the majority — rewire has moved quickly, so most of the items that used to live here have landed.

## Shipped

### Function mocking (toolexec)

Replace any function — your own, third-party, stdlib — with a closure at test time. No interfaces, no DI, no unsafe runtime patching. Works for generic functions on a per-instantiation basis. See [Function Mocking](function-mocking.md).

### Method mocking (global)

Mock struct methods via Go method expressions (`(*Type).Method` or `Type.Method`). The replacement takes the receiver as its first parameter. Applies to every instance of the type. See [Method Mocking](method-mocking.md).

### Per-instance method mocks

`rewire.InstanceFunc` scopes a method replacement to one specific receiver, leaving other instances to run the real method body (or a global mock, if one is set). Works on non-generic and generic types. Backed by a per-method `_ByInstance sync.Map` the rewriter emits on demand. See [Per-instance method mocks](method-mocking.md#per-instance-method-mocks).

### Interface mocks via `rewire.NewMock[T]`

Synthesize a concrete backing struct for an interface at compile time, triggered purely by a reference in a test. No `go:generate`, no committed `mock_*_test.go` files, no separate CLI invocation. Stubbing uses the same `rewire.InstanceFunc` verb as per-instance concrete method mocks — one API vocabulary across both. See [Interface Mocks](interface-mocks.md).

Handles non-generic interfaces, generic interfaces with arbitrary type-argument shapes (builtins, slices, maps, pointers, external-package types, nested generics), embedded interfaces (same-file, same-package, and cross-package — including generic embeds where the outer interface's type parameter flows into the embed), methods referencing same-package types as bare identifiers (auto-qualified with the declaring package alias at generation time), and interfaces declared in files that use dot imports (`import . "pkg"` — the generator detects the dot import and qualifies bare idents with the dot-imported package's alias rather than the declaring package). Package resolution goes through `go list`, so `replace` directives in `go.mod`, workspace files (`go.work`), and vendor directories all work as expected. Each instantiation produces its own backing struct keyed on `reflect.TypeFor[I]()`.

### Expectation DSL

`expect.For(t, target)` and `expect.ForInstance(t, instance, target)` layer a fluent rule-builder DSL on top of any rewire mock. Supports literal argument matching (`.On`), typed predicates (`.Match`), catch-all fallback (`.OnAny`), fixed and callback responses (`.Returns` / `.DoFunc`), call-count bounds (`.Times` / `.AtLeast` / `.Never` / `.Maybe`), and async synchronization (`.Wait`). Automatic verification at test end reports unmet bounds with source locations. See [Expectations DSL](expectations.md).

### `rewire.Real` — spy pattern

Returns the pre-rewrite implementation of a target so a mock closure can delegate to it. Works for functions, methods, generic functions, and generic methods. See [Function Mocking → Spying](function-mocking.md#spying-delegating-to-the-real-implementation).

### Mid-test cleanup — `RestoreFunc` / `RestoreInstance` / `RestoreInstanceFunc`

Three dedicated verbs covering every scope: clear a global function/method mock, clear every per-instance mock bound to one receiver, or clear one specific `(instance, method)` entry. Complements the automatic `t.Cleanup` teardown with a way to end mocks mid-test.

### Inlining compatibility (verified)

Rewire's rewrite transformation is small enough that Go's inliner inlines the wrapper into callers and `_real_X` into the wrapper, so the fast path at every call site is the original body plus a nil-check. `scripts/check-inlining.sh` runs in CI and asserts the expected inlining decisions appear in `go build -gcflags=-m=2` output, so a future compiler or rewriter change can't silently regress this.

### Generic functions and generic methods

`rewire.Func`, `rewire.Real`, `rewire.RestoreFunc`, and `rewire.InstanceFunc` all support generics on a per-instantiation basis. Each type-argument combination is dispatched independently via a sync.Map keyed on the concrete type signature. See [How It Works → Generic functions](how-it-works.md#generic-functions).

### API naming pass

The public surface has been through a coherent rename: `InstanceMethod → InstanceFunc`, the overloaded `Restore` split into `RestoreFunc` / `RestoreInstance` / `RestoreInstanceFunc`, and the unused `Replace` escape hatch removed. `expect.For` / `expect.ForInstance` were kept — they read well as DSL grammar.

## Exploratory / future ideas

Ideas we're curious about but haven't committed to. Listed here so we don't lose the thread; none of them are scheduled.

### `rewire.Package` — whole-package swap

Replace an entire package with a structurally-compatible alternative for the duration of a test, instead of mocking symbols one at a time:

```go
rewire.Package[bar](t, fakebar)  // every call into bar is routed to fakebar
```

The substitute must satisfy bar's exported API (same function signatures, same exported types, same method sets). Validation would run at compile time so a mismatch fails the build, not the test.

Open questions:

- **Scope of "public API".** Just top-level functions, or also exported types, constants, vars, and method sets on exported types? Full package parity is a harder compile-time check than the current per-function rewrite.
- **State and package-level vars.** If `bar` has package-level state (sync.Once, init(), globals), does the replacement share it, mirror it, or start fresh? Each answer breaks a different set of tests.
- **Vs. `rewire.Func`.** For tests that only need to stub one or two symbols, the per-function API is simpler. Package-level swap is for cases where you'd otherwise stub 10+ symbols to cover one "seam" in the code.
- **Interaction with existing mocks.** If a test calls `rewire.Package` and `rewire.Func` on a target in that package, which wins? Probably `rewire.Func` at per-symbol resolution, but worth thinking through the dispatch order.

The mechanism would reuse the existing toolexec pipeline: scan for `rewire.Package[P]` calls, emit per-symbol dispatch wrappers in the production source of `P` (as we do today for `rewire.Func`), and route through a package-level swap variable set by the test.

### Opt-in production rewiring

Today rewire is test-only: the toolexec wrapper rewrites functions only when compiling a test binary, and the `rewire.Func` / `rewire.InstanceFunc` APIs take `*testing.T`. An experimental direction: let production code opt into the same rewiring machinery, at either function or package granularity, so runtime swaps become possible without committing to a plugin architecture up front.

Potential use cases:

- **Feature flags without wiring.** Toggle one implementation vs. another without threading a flag through function arguments or package-level state.
- **Canary / shadow traffic.** Route a small fraction of production calls through an alternative implementation for A/B comparison, with rollback being a single swap back.
- **Late-binding implementations.** Plugin-like dispatch without `plugin.Open`, since rewire's wrappers are plain Go and inline away when no swap is active.

Open questions (all serious):

- **Binary bloat and overhead.** Today the wrapper is a nil-check that inlines; a production-opt-in version has to stay just as cheap. Any added bookkeeping changes the calculus.
- **Thread safety and publication.** Tests mock before calling; production would swap live. That needs proper ordering (sync.Map, atomic.Pointer, or similar) and a story for in-flight calls during a swap.
- **Opt-in marking.** How does a function or package declare it's available for production swap? A build tag, a magic comment, a separate API? The marker has to be something the scanner can pick up without a centralized registry.
- **Security and governance.** Production rewiring is effectively arbitrary code execution from whoever can call the swap API. Any shipped version would need a clear "who is allowed to swap what" story — probably compile-time gating rather than runtime authorization.
- **Relation to the `rewire.Func` non-goal on runtime patching.** This is not runtime machine-code patching; it's compile-time rewriting + a runtime-swappable dispatch table. The non-goal stands for code patching; this idea coexists with it.

A fun experiment. Likely a separate subpackage (`rewire/live` or similar) if it ever happens, to keep the core test-focused API uncontaminated.

## Gaps we're not actively tackling

These are more fundamental than the items above. We list them so expectations are clear, not because we're working on them.

- **Parallel test safety for the same target.** `t.Parallel()` tests that mock the *same* function with different replacements will race on the shared `Mock_` variable. Parallel tests mocking *different* targets are fine.
- **Compiler intrinsics.** Functions like `math.Abs`, `math.Sqrt`, `math.Floor` are replaced by CPU instructions at the call site, bypassing any wrapper. Rewire detects these and fails with a clear error. Non-intrinsic alternatives work fine.
- **Assembly / bodyless functions.** No Go source to rewrite. Detected and rejected at compile time.
- **Unexported functions across packages.** `bar.greet` (lowercase) can only be referenced from inside package `bar`, so the `rewire.Func` call has to live in `bar`'s own `_test.go` file. Not actively tackled.

## Non-goals

Deliberate — we're not planning to add these because they'd conflict with rewire's design:

- **A full Mockito-equivalent DSL.** The `expect` package covers the common 80% with literal matching, predicates, call-count bounds, and async wait. Past that, interface-based libraries with richer DSLs are a better fit.
- **Runtime function replacement.** The whole point of the toolexec approach is that mocks are compile-time only — the production binary has no rewire overhead beyond a nil-check wrapper per mocked target.
