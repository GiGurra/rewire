# Parallel test safety — findings from the PR #6 prototype

> Summary of what we learned while building an end-to-end working
> implementation on the `proto/parallel-safety-experimental` branch
> (draft PR #6). Implementation is not intended to ship as-is; this
> doc captures the lessons so future work has a head start.

## What was proven

A `t.Parallel()` test can mock the same target as another parallel
test and each will see its own replacement, with no race on
`Mock_Foo`. Child goroutines spawned with raw `go fn()` inherit the
parent test's mock automatically. Verified end-to-end with the
full toolexec pipeline (rewriter + runtime + registration) and
under `-race`.

Performance overhead: ~7 ns per call over the current nil-check
baseline (13 → 20 ns per wrapper hit, no mock active). Benchmarks
in `pkg/rewire/parallel_experimental_bench_test.go` on the branch.

## The mechanism: pprof labels as goroutine identity

The initial plan in `plans/review_followups.md` was
goroutine-ID-keyed storage with an explicit child-handoff helper
(`rewire.Go(t, fn)`). That was built first and worked functionally
but had two problems: (a) Go 1.23+ blocks `//go:linkname
runtime.goid`, forcing a slow `runtime.Stack()` fallback, and
(b) the child-goroutine handoff was a silent-failure mode (tests
that forgot `rewire.Go` would miss the mock).

The working mechanism instead uses **pprof profile labels as a
goroutine-tree identity**:

- `runtime/pprof.SetGoroutineLabels(ctx)` sets labels on the current
  goroutine (public API, no linkname).
- The labels pointer lives on the `g` struct and is inherited by
  child goroutines at spawn time (Go runtime behavior — no explicit
  handoff needed).
- Read via `//go:linkname runtime/pprof.runtime_getProfLabel` —
  runtime pushes the symbol out to pprof, and pprof's stub is
  linker-reachable for third-party packages. This is the one
  unofficial reach-in, but it's the only one.

The **labels pointer value** becomes our goroutine-tree identity
key. Different tests get different pointers. Children inherit
parent pointers.

## Architecture: per-package state, one cross-package linkname

Earliest version tried a central `rewire.LookupMock` function that
all rewritten packages linknamed to. That failed at link time:
when a test binary doesn't transitively import rewire (e.g. a test
in `internal/toolexec` whose test files don't use `rewire.Func` but
whose deps include a stdlib package that some *other* test mocks),
the linker can't resolve `rewire.LookupMock`.

Final architecture is **per-package state, self-contained**:

- Each rewritten function gets its own `Mock_Foo_ByGoroutine
  sync.Map` declared in the rewritten file.
- A sidecar `_rewire_linkname.go` per rewritten package holds the
  one linkname to `runtime/pprof.runtime_getProfLabel`.
- `rewire.Func` writes to the per-function map via a new
  registration (`rewire.RegisterGoroutineMap`), same pattern as
  existing `rewire.Register`.

This means no test binary needs rewire in its link set because of
rewire's rewriting decisions — the rewritten code only reaches into
`runtime/pprof`, which every Go binary already links.

## Inheritance / override semantics

Concrete answer to "what happens when the child overrides"
(relevant for evaluating this as general-purpose GLS):

- Children inherit parent's labels pointer **at spawn time only**
  (single pointer assignment to `g.labels`; no ongoing link).
- `SetGoroutineLabels` in the child is **strictly local** — it
  replaces the child's own pointer without affecting the parent.
- To augment parent's data while keeping parent keys visible, the
  child needs to call `pprof.WithLabels(parentCtx, ...)` — which
  requires having the parent's `context.Context`. rewire doesn't
  care (it uses the pointer as an identity, not as data storage),
  but a GLS library would.

## Ecosystem survey

Surveyed open-source Go GLS / goid libraries (petermattis/goid,
huandu/go-tls, modern-go/gls, jtolio/gls, timandy/routine,
outrigdev/goid, others). Only `timandy/routine` would meaningfully
benefit from the pprof-stub-linkname finding — its
`InheritableThreadLocal` feature wants child inheritance and
currently achieves it via ASM + a separate toolexec compiler
plugin. Everyone else wants strict isolation (inheritance would be
wrong) or only needs bare ID lookup (ASM is faster).

Decided not to evangelize the finding upstream: it's a linker
loophole, not a documented API, and wide promotion could
accelerate Go closing it. Preserved here as reference.

## Why PR #6 isn't merge-ready

- **Linkname loophole is unofficial.** Future Go releases could
  restrict it further. Fallback would be either `runtime.Stack()`
  parsing (1000× slower, visible regression) or inline ASM
  (architecture-specific, per-Go-version offset tables).
- **Sidecar file injected into every rewritten package.** Not a
  huge deal functionally, but it's another thing the toolexec
  machinery has to produce and track.
- **Pure goroutine-local mode changes semantics.** `rewire.Func`
  stops writing to `Mock_Foo` when a map is registered. Anything
  reading `Mock_Foo` directly (not through the wrapper) stops
  seeing mocks. Nothing in the tree does this today but it's a
  real behavior change.
- **pprof profiling interference.** `rewire.Func` calls
  `SetGoroutineLabels` as a side effect. If a user runs
  `go test -cpuprofile`, our `rewire.scope` label shows up in
  their output. Minor noise, filterable by label key.
- **Generic functions not yet covered.** The goroutine-local path
  only kicks in for the non-generic rewriter branches. Generic
  functions keep sync.Map-by-type-signature, which is already
  per-instantiation-safe but still races on same-instantiation
  parallel mocks.
- **Decisions pending on user-facing API.** Always-on (as the PR
  does it) vs opt-in via scanner-detected `t.Parallel()` vs a
  separate `rewire.FuncParallel` verb. The "automatic opt-in" idea
  (only flag targets mocked by tests that call Parallel()) is
  clean but not implemented.

## Pointers for a future merge attempt

1. Decide if the linkname-through-pprof dependency is acceptable
   risk for rewire's stability commitments.
2. Decide on API shape — always-on rewrites existing behavior
   slightly; opt-in keeps legacy path cheap but adds a verb.
3. Benchmark on a real-world-sized module (not just the examples)
   to confirm the 7 ns overhead is acceptable in practice.
4. Extend to generic functions if the constraint bites — the
   same labels-pointer-keyed scheme should compose with the
   existing per-type-signature dispatch.
5. Consider `timandy/routine`'s ASM approach as a fallback if the
   Go team closes the linkname loophole.

See PR #6 for the implementation and `docs/limitations.md` → "No
parallel mock safety" for the current documented state.
