# TODO — Naming / API consistency pass (after feature scope settles)

**Status:** deliberately deferred
**When to tackle:** after Phase 2/3 of toolexec interface mocks lands and the full feature set is stable. Do NOT do this piecemeal — one coherent pass is cheaper than a trickle of breaking renames.

## Why this is deferred

Rewire has grown organically:

- `rewire.Func` — the original and only verb for a while
- `rewire.Real` — the escape hatch for spy-style tests
- `rewire.Restore` — optional mid-test cleanup
- `rewire.Replace` — low-level direct swap, rarely used
- `rewire.InstanceMethod` — per-receiver scoped method mock
- `rewire.RestoreInstanceMethod` — drop one per-instance entry
- `rewire.NewMock[T]` — toolexec-generated interface mock
- `rewire.Register` / `RegisterReal` / `RegisterByInstance` / `RegisterMockFactory` — compile-time glue that users never call
- `expect.For` / `expect.ForInstance` — DSL entry points
- `expect.Rule.On` / `OnAny` / `Match` / `Returns` / `DoFunc` / `Times` / `AtLeast` / `Never` / `Maybe` / `Wait` — rule builders

This works but it isn't *consistent*. A user has to learn why "mock a global function" is `Func` but "mock a specific instance's method" is `InstanceMethod` and not (say) `FuncOn` or `MethodOn`. They have to learn that `Real` returns the pre-rewrite impl while `Restore` clears a mock. They have to learn that `expect.For` wraps method expressions while `expect.ForInstance` wraps both concrete instances and interface mocks. None of this is hard, but it's more vocabulary than the underlying concepts deserve.

The reason to defer the cleanup is **we don't yet know the full verb set**. Phase 2 and Phase 3 of interface mocks will probably introduce at least one more public function. Renaming twice is worse than renaming once after everything is in.

## What a cleaned-up API might look like

Brainstorming only — do not implement until the scope is final. Two main axes worth considering:

### Axis 1: Collapse `Func` / `Method` / `InstanceMethod` into one verb

Something like:

```go
rewire.Mock(t, target, replacement)                // global — same as Func today
rewire.Mock(t, target, replacement, rewire.On(s1)) // per-instance — options pattern
```

One verb, optional per-instance scope via an option. Symmetry:

```go
rewire.Restore(t, target)        // global
rewire.Restore(t, s1)            // all per-instance mocks on s1 (already works)
rewire.Restore(t, target, rewire.On(s1)) // one per-instance entry
```

Tradeoff: options feel less direct than a named function. `InstanceMethod` reads well on its own today; a `Mock(..., On(s1))` call reads more like configuration. Need to weigh clarity vs. surface area.

### Axis 2: Rename `Real` to something more obvious

`Real` is fine once you know it, but first-time readers ask "real what?" Possibilities:

- `rewire.Original` — "the original, pre-rewrite function"
- `rewire.Unmocked` — explicit about what's being returned
- `rewire.Passthrough` — matches the intent (you usually call it to pass through from inside a mock)

No strong preference yet. Decide after seeing more user code that imports `rewire.Real` — if it reads naturally in context, leave it alone.

### Axis 3: Consider whether `NewMock` belongs under a different verb

`rewire.NewMock[T]` returns an instance. Most other rewire verbs install side effects and return nothing (or a helper). `NewMock` is the outlier. Options:

- Leave it. Constructor-style names starting with `New` are idiomatic Go.
- Move it under a namespace: `rewire.Mock.New[T]()`. Feels over-engineered.
- Rename to something that signals "mock instance": `rewire.Instance[T]` or `rewire.Stub[T]`. Both read well but `Instance` clashes with `InstanceMethod` and `Stub` has connotations in other mocking libraries we'd have to defend against.

Probably leave it alone.

### Axis 4: Unify `expect.For` and `expect.ForInstance`

Same collapse as Axis 1:

```go
expect.For(t, target)                                // global — unchanged
expect.For(t, target, expect.On(instance))           // per-instance
```

Or keep them as separate verbs because the distinction matters for dispatch semantics.

## What NOT to change

- `rewire.Func` is too established to break without real need. If we consolidate, keep `Func` as an alias for the unified verb for at least one deprecation cycle.
- `expect.For` is similarly established. Same treatment.
- The internal `Register*` helpers are generated-code-only — users never type them. Renames there are free but low-value.

## Process when the time comes

1. Write out the full current verb list and the proposed one side-by-side.
2. Draft a migration path for every rename (keep the old names as deprecated aliases where reasonable).
3. Bulk-rename with gopls rename (not string replace) so references update cleanly.
4. Update every code block in every doc, README, and example test.
5. Bump the minor version, note the renames in release notes.

## Not yet, just later

The important thing is to let this sit until we know the full shape of the library. Premature consistency is still premature — you end up re-consistency-ing later anyway.
