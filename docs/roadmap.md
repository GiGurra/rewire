# Roadmap

This page tracks rewire's shipped features, what's actively planned, and gaps we're deliberately not tackling. "Shipped" is the majority — rewire has moved quickly, so most of the items that used to live here have landed.

## Shipped

### Function mocking (toolexec)

Replace any function — your own, third-party, stdlib — with a closure at test time. No interfaces, no DI, no unsafe runtime patching. Works for generic functions on a per-instantiation basis. See [Function Mocking](function-mocking.md).

### Method mocking (global)

Mock struct methods via Go method expressions (`(*Type).Method` or `Type.Method`). The replacement takes the receiver as its first parameter. Applies to every instance of the type. See [Method Mocking](method-mocking.md).

### Per-instance method mocks

`rewire.InstanceMethod` scopes a method replacement to one specific receiver, leaving other instances to run the real method body (or a global mock, if one is set). Works on non-generic and generic types. Backed by a per-method `_ByInstance sync.Map` the rewriter emits on demand. See [Per-instance method mocks](method-mocking.md#per-instance-method-mocks).

### Interface mocks via `rewire.NewMock[T]`

Synthesize a concrete backing struct for an interface at compile time, triggered purely by a reference in a test. No `go:generate`, no committed `mock_*_test.go` files, no separate CLI invocation. Stubbing uses the same `rewire.InstanceMethod` verb as per-instance concrete method mocks — one API vocabulary across both. See [Interface Mocks](interface-mocks.md).

Handles non-generic interfaces, generic interfaces with arbitrary type-argument shapes (builtins, slices, maps, pointers, external-package types, nested generics), embedded interfaces (same-file, same-package, and cross-package — including generic embeds where the outer interface's type parameter flows into the embed), methods referencing same-package types as bare identifiers (auto-qualified with the declaring package alias at generation time), and interfaces declared in files that use dot imports (`import . "pkg"` — the generator detects the dot import and qualifies bare idents with the dot-imported package's alias rather than the declaring package). Package resolution goes through `go list`, so `replace` directives in `go.mod`, workspace files (`go.work`), and vendor directories all work as expected. Each instantiation produces its own backing struct keyed on `reflect.TypeFor[I]()`.

### Expectation DSL

`expect.For(t, target)` and `expect.ForInstance(t, instance, target)` layer a fluent rule-builder DSL on top of any rewire mock. Supports literal argument matching (`.On`), typed predicates (`.Match`), catch-all fallback (`.OnAny`), fixed and callback responses (`.Returns` / `.DoFunc`), call-count bounds (`.Times` / `.AtLeast` / `.Never` / `.Maybe`), and async synchronization (`.Wait`). Automatic verification at test end reports unmet bounds with source locations. See [Expectations DSL](expectations.md).

### `rewire.Real` — spy pattern

Returns the pre-rewrite implementation of a target so a mock closure can delegate to it. Works for functions, methods, generic functions, and generic methods. See [Function Mocking → Spying](function-mocking.md#spying-delegating-to-the-real-implementation).

### `rewire.Restore` — mid-test cleanup

Overloaded to accept either a function target (clears the global mock) or an instance value (clears every per-instance mock scoped to that instance). Complements the automatic `t.Cleanup` teardown with a way to end mocks mid-test.

### Inlining compatibility (verified)

Rewire's rewrite transformation is small enough that Go's inliner inlines the wrapper into callers and `_real_X` into the wrapper, so the fast path at every call site is the original body plus a nil-check. `scripts/check-inlining.sh` runs in CI and asserts the expected inlining decisions appear in `go build -gcflags=-m=2` output, so a future compiler or rewriter change can't silently regress this.

### Generic functions and generic methods

`rewire.Func`, `rewire.Real`, `rewire.Restore`, and `rewire.InstanceMethod` all support generics on a per-instantiation basis. Each type-argument combination is dispatched independently via a sync.Map keyed on the concrete type signature. See [How It Works → Generic functions](how-it-works.md#generic-functions).

## In flight / planned

### API naming pass

Once the full feature scope is stable, a coherent rename + consolidation pass to address vocabulary drift across `Func` / `InstanceMethod` / `NewMock` / `expect.For` / `expect.ForInstance`. Deliberately deferred until the library's surface area has settled — piecemeal renames are worse than one coherent sweep. See `plans/TODO_next_session.md`.

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
