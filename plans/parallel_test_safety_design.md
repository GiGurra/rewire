# Parallel test safety — design exploration

> Status: design exploration, not a committed roadmap item. Captures
> the option space so a future implementation doesn't start from a
> blank page. Companion prototype lives in a separate draft PR.

## Current state

Rewire today documents a single parallel-safety limitation: two
`t.Parallel()` tests in the same package that call `rewire.Func` on
**the same non-generic target** will race on the package-level
`Mock_Foo` variable. See `docs/limitations.md` → "No parallel mock
safety".

It's worth being precise about what this rules in and out, because
most of the parallel-safety space is **already covered** by existing
rewire features:

| Pattern                                          | Parallel-safe today? | Why                                                                 |
|--------------------------------------------------|----------------------|---------------------------------------------------------------------|
| `rewire.Func(t, bar.Greet, ...)` on same target  | ❌ no                | Shared `Mock_Greet` var                                             |
| `rewire.Func(t, bar.Greet, ...)` on diff targets | ✅ yes               | Different `Mock_` vars, no contention                               |
| `rewire.Func(t, bar.Map[int, string], ...)`      | ✅ yes per-inst      | Keyed in `sync.Map` by instantiation — different instantiations ok, same instantiation still races |
| `rewire.Func(t, (*T).M, ...)` (global method)    | ❌ no                | Same as `rewire.Func` on a function                                 |
| `rewire.InstanceFunc(t, inst, (*T).M, ...)`      | ✅ yes               | Keyed in `_ByInstance sync.Map` by receiver identity                |
| `rewire.NewMock[I](t)` + `InstanceFunc`          | ✅ yes               | Each test gets its own mock instance; `NewMock` returns a fresh pointer |

So the parallel-safety "gap" narrows dramatically: **anything that
goes through per-instance dispatch is already parallel-safe**, because
the dispatch key includes receiver identity and every test creates
its own receiver. The unsolved case is specifically:

1. Free functions mocked via `rewire.Func`
2. Global method mocks (same mechanism as 1 for the rewriter)
3. Generic free functions mocked on the same instantiation

For a lot of real-world code, (3) is rare, (2) has a mechanical
migration path to `InstanceFunc`, and (1) is the honest target of
this exploration.

## The hard constraint

Go doesn't expose a public way to attach data to the currently
running goroutine. The runtime has a goroutine ID internally, but:

- `runtime.Goid` is unexported and has been kept deliberately so
- `runtime/debug.Stack()` + string parsing works but is slow and ugly
- `//go:linkname runtime.goid` compiles and works, but is
  unsanctioned — Go team has broken private linkname access before
  (Go 1.23 tightened the rules) and will likely keep doing so

That rules out the clean "just look up the current test" approach
every other language's mocking libraries take. The design options
all have to work within this constraint.

## Options considered

### A. Goroutine-local storage via `go:linkname runtime.goid`

A `sync.Map[goid]*mockTable` at the rewire runtime level. Each test
that calls a new `rewire.FuncParallel` API installs its mocks into
its goid's entry. The rewriter emits an extra lookup ahead of the
existing nil check.

**Call-time shape**:
```go
func Greet(name string) string {
    if gmocks := getGoroutineMocks(); gmocks != nil {
        if m, ok := gmocks.Load("pkg.Greet"); ok {
            return m.(func(string) string)(name)
        }
    }
    if _rewire_mock := Mock_Greet; _rewire_mock != nil {  // existing
        return _rewire_mock(name)
    }
    return _real_Greet(name)
}
```

**Pros**: tests get parallel safety without any API contortion;
per-test isolation is automatic.

**Cons**:
- `go:linkname runtime.goid` is fragile across Go versions
- Every call into a rewritten function pays an extra `sync.Map.Load`
  on the hot path — ~50–100 ns. On functions that aren't mocked at
  all, that's a real regression for non-parallel tests too.
- **Child goroutines do not inherit the parent's mocks.** A test
  that `go func() { bar.Greet(...) }()` won't see the goroutine-
  local mock in the child because Go goroutines don't carry any
  inherited state.

The child-goroutine problem is the killer. Fixing it requires
either a `rewire.Go(t, func(){...})` helper that copies the parent's
mock table onto the child's goid before the user's fn runs, or
wrapping the Go spawn primitive (impossible — `go` is a keyword).

The helper approach works but has the same "call this helper
correctly or things silently don't work" shape that interface-based
DI already has — arguably worse, because the failure mode is "the
mock is silently inactive" rather than a compile error.

### B. Per-test mock tables, opt-in at compile time

Same as A, but make it opt-in: a separate scanner verb
(`rewire.FuncParallel(t, bar.Greet, ...)`) signals the rewriter to
emit the goroutine-local lookup *only for that target*. Functions
mocked only via `rewire.Func` get the current cheap nil-check
wrapper.

**Pros**: zero cost for code that doesn't need parallel safety. The
additional complexity is isolated to targets that explicitly opt in.

**Cons**: still needs `go:linkname runtime.goid`; still has the
child-goroutine handoff problem; now has two wrapper shapes the
rewriter has to maintain.

This is the most realistic direction if we ship anything. The
"pay-for-what-you-use" shape matches rewire's existing design
(e.g. `_ByInstance` sync.Map is only emitted when
`rewire.InstanceFunc` is used on a target).

### C. Context-threaded API

Every mockable function takes an implicit `context.Context` that
carries test identity.

**Rejected.** Requires rewriting every production function's
signature. Defeats the core value proposition ("mock any function
without changing production code").

### D. Per-test mock tables via caller stack-walk

At dispatch time, walk `runtime.Callers` looking for a frame whose
receiver is a `*testing.T`.

**Rejected.** An order of magnitude slower than goid lookup,
confounded by inlining and interface dispatch, and still subject to
the child-goroutine problem (the child's stack doesn't include the
parent test's frames).

### E. Status quo, improve diagnostics

Don't implement parallel safety. Instead, add a runtime check: when
`Mock_Foo` is assigned from more than one goroutine simultaneously,
detect and fail loudly (e.g. via `-race`, or a counter guarded by
`atomic`).

**Pros**: no rewriter changes, no unsanctioned runtime access, no
API surface growth. Turns a silent race into a visible failure.

**Cons**: doesn't *solve* the problem — users still can't
parallelize those tests. It just makes the existing limitation
harder to trip on silently.

Worth considering as a complement to A or B, not a replacement.

## Recommendation

If this is ever built, **option B (opt-in, scanner-driven, goid-
local with `rewire.Go` handoff)** is the shape that fits rewire's
existing design. Key ingredients:

1. A new `rewire.FuncParallel[F any](t *testing.T, target F,
   replacement F)` verb. Same surface as `rewire.Func` but signals
   "this target should be isolated per-goroutine."
2. Scanner extension: detect `rewire.FuncParallel` calls, build a
   `parallelTargets` set alongside the existing `byInstance` set,
   feed to the rewriter.
3. Rewriter extension: for targets in `parallelTargets`, emit a
   wrapper with the goroutine-local lookup ahead of the existing
   nil check. Non-parallel targets get the current wrapper
   unchanged.
4. Runtime extension: `pkg/rewire/parallel.go` with goroutine-local
   storage keyed on `runtime.goid` via `go:linkname`, plus
   `rewire.Go(t, fn)` for explicit parent-to-child mock handoff.
5. Documentation + diagnostics: limitation page clearly states
   "child goroutines must use `rewire.Go` or they see unmocked
   behavior," and the wrapper optionally logs when it falls through
   to the global path on a parallel target (helpful for detecting
   missed `rewire.Go` handoffs during development).

Explicitly **not** recommended as a default: making all
`rewire.Func` calls goroutine-local. The hot-path cost and the
child-goroutine pitfall both outweigh the benefit in the common
case (sequential tests).

## Cost analysis

Per-call overhead for a mocked function, with no mock set:

| Wrapper shape                               | Overhead       | Notes                           |
|---------------------------------------------|----------------|---------------------------------|
| Current (non-generic)                       | ~2 ns          | Nil check, branch-predicted     |
| Current (generic)                           | ~100s of ns    | reflect + sync.Map              |
| Goroutine-local added (non-parallel target) | Same as curr   | Only parallel targets pay       |
| Goroutine-local added (parallel target)     | +50–100 ns     | goid lookup + sync.Map.Load     |

The opt-in shape means the marginal cost lands only on targets the
user asked to make parallel-safe. Aligns with how `_ByInstance`
already works.

## Prototype scope

A useful prototype proves:

1. Goroutine-local mock storage works: a test can install a mock
   and see the mocked value from the same goroutine.
2. Two parallel goroutines installing different mocks for the same
   target don't interfere.
3. `rewire.Go(t, fn)` carries the mock into a child goroutine, and
   omitting it drops back to the global fallback.
4. Non-parallel targets are not affected (they go through the
   existing wrapper unchanged).

What the prototype does **not** need:

- Scanner integration (`rewire.FuncParallel` detection)
- Rewriter integration (generating the new wrapper shape)
- Tight coupling to `*testing.T` (the prototype can work off any
  "scope" the caller provides; wiring to `testing.T` is easy)

The prototype can live entirely in `pkg/rewire/` as a new file
(e.g. `parallel_experimental.go`) plus a test file that
demonstrates the properties by hand, without touching the rewriter.
That keeps the proof-of-concept self-contained and reviewable.

## Open questions

- **Linkname stability**: what's the cost of `go:linkname` breaking
  on a future Go release? How much notice would we have?
  - Mitigation: if the linkname trick stops working, the fallback
    path (global `Mock_Foo`) still works — code using
    `FuncParallel` would just see it behave exactly like `Func`.
    Ugly but not catastrophic.
- **Child-goroutine handoff ergonomics**: is `rewire.Go` acceptable,
  or is a "it just works" child inheritance required? If the
  latter, we're probably stuck — Go doesn't give us a hook.
- **Should `rewire.Func` become an alias for `rewire.FuncParallel`
  on `-race` builds?** Would catch missed parallel-safety bugs
  under race mode without changing the production call pattern.
  Probably no — the child-goroutine pitfall would be invisible
  outside `-race`, which is worse.
- **Diagnostic mode**: should the global-fallback path on a
  parallel target emit a `t.Log` when hit from a non-registered
  goroutine? Helpful for spotting missed `rewire.Go` handoffs, but
  only if `*testing.T` is accessible at the wrapper level (it
  isn't, without plumbing).

## References

- Relevant rewire source: `pkg/rewire/rewire.go`
  (`Func`, `InstanceFunc`, existing registries)
- Existing doc: `docs/limitations.md` → "No parallel mock safety"
- Existing doc: `docs/design.md` → "Future work" → "Parallel test
  safety"
- Go runtime: `runtime.goid` (unexported, used by runtime internals)
- Prior art: `pprof.Do` / `pprof.Label` — context-based approach to
  per-goroutine state (ruled out because it requires threading
  context through every call)
