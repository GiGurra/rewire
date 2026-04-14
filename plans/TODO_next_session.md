# TODO — Next session

Two coupled reviews to do as one coherent pass:

## 1. API naming review

Go through every public identifier in `pkg/rewire` and `pkg/rewire/expect`
and evaluate whether the verb set is consistent. The full current
surface:

**`pkg/rewire`:**

- `Func[F](t, original, replacement)` — replace a function/method
- `InstanceMethod[I, F](t, instance, original, replacement)` — per-receiver method mock
- `NewMock[I](t)` — synthesized interface mock
- `Real[F](t, original)` — pre-rewrite impl (spy pattern)
- `Restore[T](t, target)` — overloaded: function target or instance value
- `RestoreInstanceMethod[I, F](t, instance, original)` — clear one per-instance entry
- `Replace[F](t, &target, replacement)` — low-level direct swap
- `Register` / `RegisterReal` / `RegisterByInstance` / `RegisterMockFactory` — codegen-only, users never call

**`pkg/rewire/expect`:**

- `For[F](t, target)` — DSL on global function/method mocks
- `ForInstance[F](t, instance, target)` — DSL on per-instance / interface mocks
- `(*Expectation).On(args...)` / `.OnAny()` / `.Match(pred)` — match rules
- `(*Rule).Returns(vals...)` / `.DoFunc(fn)` — responses
- `(*Rule).Times(n)` / `.AtLeast(n)` / `.Never()` / `.Maybe()` — call-count bounds
- `(*Rule).Wait(count, timeout)` — async sync
- `(*Expectation).AllowUnmatched()` — pass-through fallback

**Questions to answer in the review:**

- Is `Func` the right primary verb, or does it read oddly when the
  target is a method expression (e.g. `rewire.Func(t, (*bar.Server).Handle, …)`)?
- Should `Restore`'s overload (function vs instance) split into two
  explicit functions? It's cute but ambiguous in docs.
- Does `Real` read well? `rewire.Real(t, fn)` returns a function,
  but the name sounds like a noun.
- Should `expect.For` and `expect.ForInstance` unify? The only
  difference is how the mock gets scoped.
- Any Register* helpers that leak into godoc and confuse readers?
  (They're public because codegen emits calls to them, but users
  shouldn't see them.)
- Are the match/response method names stable enough to commit to?
  `On(args)` feels right for literal match; `OnAny()` vs `Any()`?

**Do NOT do piecemeal renames.** One coherent PR, mechanical sweep
across tests + docs + examples. Bumping the module is fine since
we're still pre-1.0.

### Brainstorming axes (think before deciding)

Candidate directions, non-binding — all need a gut-check against
real user code before committing.

**Axis 1: collapse `Func` / `InstanceMethod` into one verb.**

```go
rewire.Mock(t, target, replacement)                     // global — same as Func today
rewire.Mock(t, target, replacement, rewire.On(s1))      // per-instance via option
rewire.Restore(t, target)                               // global
rewire.Restore(t, s1)                                   // all per-instance mocks on s1
rewire.Restore(t, target, rewire.On(s1))                // one per-instance entry
```

Trade-off: an options pattern reads like configuration, while
`InstanceMethod` reads as a dedicated verb. Options win on
surface-area count; named verbs win on discoverability and docs
readability. No clear answer yet.

**Axis 2: rename `Real` to something more obvious.**

`Real` is fine once you know it, but first-time readers ask "real
what?" Candidates: `Original`, `Unmocked`, `Passthrough`. Decide
after reading a representative sample of test code that uses it —
if `rewire.Real(t, fn)` reads naturally in context, leave it.

**Axis 3: whether `NewMock` belongs under a different verb.**

`rewire.NewMock[T]` returns an instance — all other rewire verbs
install side effects. Options:

- Leave it. Constructor-style names starting with `New` are idiomatic Go.
- Namespace it: `rewire.Mock.New[T]()`. Feels over-engineered.
- `rewire.Instance[T]` / `rewire.Stub[T]`. `Instance` clashes
  with `InstanceMethod`; `Stub` has loaded connotations from
  other mocking libraries.

Probably leave alone, but validate during the review.

**Axis 4: unify `expect.For` and `expect.ForInstance`.**

Same collapse as Axis 1:

```go
expect.For(t, target)                        // global — unchanged
expect.For(t, target, expect.On(instance))   // per-instance
```

Or keep them separate because the dispatch semantics genuinely
differ (global mock variable vs per-receiver `sync.Map`).

### What NOT to change

- **`rewire.Func`** is too established. If we consolidate, keep
  `Func` as a deprecated alias for the unified verb for at least
  one release.
- **`expect.For`** is similarly established. Same treatment.
- The internal `Register*` helpers are generated-code-only —
  users never type them. Renames there are free but low-value.
- Match / response / bound method names on `*Rule` (`On`, `OnAny`,
  `Match`, `Returns`, `DoFunc`, `Times`, `AtLeast`, `Never`,
  `Maybe`, `Wait`) feel stable and read well in chains.

### Process when we do the rename

1. Write the full current verb list and the proposed one side by side.
2. Draft a migration path for every rename (deprecated aliases
   where reasonable).
3. Bulk-rename with gopls/LSP rename, not string replace, so
   references update cleanly.
4. Update every code block in every doc, README, and example test.
5. Bump the minor version, note the renames in release notes.

## 2. Docs review — end-to-end coherence pass

Read every file under `docs/` in nav order and sanity-check that
the whole thing flows as one document. Things to watch for:

- **Internal link integrity**: we've moved and merged sections a
  lot recently. Cross-references may have gone stale.
- **Duplication**: `docs/index.md`, `docs/getting-started.md`,
  `README.md`, and `docs/function-mocking.md` all have quick-start
  snippets. Are they consistent? Does each earn its place or is
  one redundant?
- **Design notes vs how-it-works**: now that `design.md` is in the
  nav, there's some overlap with `how-it-works.md`. Each should
  have a clear role (design = "why and alternatives considered",
  how-it-works = "what happens step by step").
- **Performance section tone**: after the recent rewrite, it's
  honest but longer than the other sections. Maybe trim.
- **Limitations**: should ONLY describe things that don't work.
  Verify no capability descriptions have crept back in.
- **Roadmap**: most Phase 2 items are shipped. The roadmap page
  should reflect that cleanly without a long trail of "shipped"
  markers.
- **Naming consistency with the API review**: whichever verbs win
  in #1, make sure the docs use them uniformly. Do #1 first.

**Order of operations:** do the API review first, so docs naturally
fall out of it. Docs pass is partly mechanical (find/replace the
renames) and partly editorial (rewrite prose where verbs changed).

## Out of scope (for now)

- Scan cache cleanup (disk leak + PID reuse edge case). Discussed
  earlier, deliberately skipped.
- Any further performance optimization. The cost is fundamental to
  compiling generated mock code; not worth chasing.
- Parallel test safety for the same target. Still documented as a
  limitation.
