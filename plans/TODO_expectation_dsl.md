# TODO — Expectation DSL (opt-in)

**Status:** ✅ shipped. See `pkg/rewire/expect/`, `example/foo/expectations_test.go`, and [docs/expectations.md](../docs/expectations.md).
**Original survey item:** #4
**Layer:** pure-Go opt-in layer on top of `rewire.Func` — no rewriter, scanner, or toolexec changes.

## Motivation

Rewire's current mocking style is "write your own closure with a counter." That's fine for light use but gets tedious when a test verifies many interactions. Libraries like gomock / testify/mock give you declarative expectations:

```go
mock.EXPECT().Get("alice").Return("hi alice").Times(1)
mock.EXPECT().Get(gomock.Any()).Return("default")
```

Users coming from gomock want something similar. Rewire can offer it as an **opt-in layer** so users who prefer the closure style pay nothing.

## Scoping principles

- **Opt-in.** Lives in a separate package (`pkg/rewire/expect`). Users who don't import it see no new API surface and pay no runtime cost.
- **Layer, not a rewrite.** Built on top of `rewire.Func` + reflect. No changes to the rewriter, scanner, toolexec, or codegen.
- **No IDE red squiggles.** All identifiers in user code resolve via `gopls` like any other package. No rewriter-synthesized symbols leak into user-visible source.
- **Minimal first.** Ship a small useful subset, expand based on real demand. Resist the temptation to build a full gomock-equivalent up front.

## How it works (behind the scenes)

`expect.For(t, bar.Greet)` **is the mocking step** — not a passive "collect expectations for later." It's an alternative higher-level entry point to `rewire.Func`, not an addition to it. Under the hood:

1. Allocate a new `Expectation[F]` state object (holds the rule list, call counts, mutex, etc.).
2. Build a dispatcher closure via `reflect.MakeFunc` whose type is `reflect.TypeOf(original)`. The closure's body looks up the matching rule in the state and produces a response.
3. **Call `rewire.Func(t, original, dispatcher)`** — this is where the mock actually gets wired in.
4. Register a `t.Cleanup` that runs verification (missed expectations, unmatched inputs). Cleanups run LIFO, so this runs *before* `rewire.Func`'s own restore cleanup, which is what we want.
5. Return the `*Expectation[F]` so the caller can keep building rules.

From the moment `expect.For` returns, the target is mocked. Each subsequent `.On(...)`, `.Match(...)`, `.OnAny()` call appends to the rule list under a mutex; the dispatcher re-reads the list on every call, so later additions are visible to later calls.

**You never write `rewire.Func(t, bar.Greet, ...)` alongside `expect.For(t, bar.Greet)`.** That would install two mocks for the same target, and the second would clobber the first.

## Proposed API

### Basic usage

```go
import (
    "testing"

    "github.com/GiGurra/rewire/pkg/rewire/expect"
    "example/bar"
)

func TestWelcome_WithExpectations(t *testing.T) {
    e := expect.For(t, bar.Greet)

    e.On("Alice").Returns("hi Alice")
    e.On("Bob").Returns("hi Bob")
    e.OnAny().Returns("hi other")   // catch-all fallback

    // ... code under test exercises bar.Greet ...

    // Automatic verification via t.Cleanup:
    //   - rules with strict bounds (the default) fail if unmatched
    //   - unmatched calls fail the test at call time
}
```

### Multiple patterns

Multiple rules with different match patterns are the core use case:

```go
e := expect.For(t, bar.Greet)

e.On("Alice").Returns("hi Alice")       // rule 0 — literal "Alice"
e.On("Bob").Returns("hi Bob")           // rule 1 — literal "Bob"
e.Match(func(n string) bool {           // rule 2 — typed predicate (full IDE type-check)
    return strings.HasPrefix(n, "admin_")
}).Returns("admin")
e.OnAny().Returns("hi other")           // rule 3 — catch-all

bar.Greet("Alice")     // → rule 0 → "hi Alice"
bar.Greet("Bob")       // → rule 1 → "hi Bob"
bar.Greet("admin_42")  // → rule 2 → "admin"
bar.Greet("Xyz")       // → rule 3 → "hi other"
```

Dispatch is **first-fit in declaration order**. `.OnAny()` should be declared last, otherwise it'd swallow everything.

### Call-count bounds

Every rule has an implicit bound that `t.Cleanup` verifies, plus explicit overrides:

```go
e.On("Alice").Returns("hi").Times(3)    // exactly 3
e.On("Bob").Returns("hi").AtLeast(1)    // ≥ 1
e.On("Debug").Returns("hi").Maybe()     // 0 or more — opt out of strict default
e.On("Never").Never()                   // must not be called at all
```

**Default strictness:**

| Rule kind | Default bound | Reasoning |
|---|---|---|
| `.On(args)` | strict — must match ≥ 1 call | "I expect this specific arg to be passed" |
| `.Match(predicate)` | strict — must match ≥ 1 call | "I expect a call satisfying this predicate" |
| `.OnAny()` | lenient — 0 to ∞ | it's a catch-all, zero matches is fine |

This gives "was the mock actually called?" coverage **for free**: if you write `e.On("Alice").Returns("hi")` and nothing ever calls `bar.Greet("Alice")`, the test fails at cleanup with a clear message:

```
rewire/expect: expectation bar.Greet rule #0 (.On("Alice")) was not called
  expected: at least 1
  got:      0
```

### Side effects and spying

Rules can run arbitrary code instead of returning canned values:

```go
var calls []string
e.OnAny().Do(func(name string) string {
    calls = append(calls, name)
    return "recorded"
})
```

Or delegate to the real implementation via `rewire.Real`:

```go
realGreet := rewire.Real(t, bar.Greet)
e.On("Alice").DoFunc(func(name string) string {
    return realGreet(name) + " [spied]"
})
```

### Unmatched calls

If a call arrives that matches no rule (and there's no `.OnAny()` catch-all), the dispatcher fails the test at call time with a clear message showing the unmatched arguments. Opt-out:

```go
e := expect.For(t, bar.Greet).AllowUnmatched() // unmatched → pass through to real
```

### Explicitly NOT in v1

- `gomock.InOrder(...)` call-ordering graphs — use `.Times(n)` bounds and the first-fit ordering for simple cases.
- A full matcher algebra (`gomock.Any()`, `gomock.Eq(x)`, `gomock.Not(m)` composition) — use `.Match(predicate)` as the escape hatch; predicates compose naturally in Go.
- `.InOrder` across multiple functions.
- Verification hooks outside `t.Cleanup`.

## Type safety story

Go's generics are too weak to fully decompose a function type `F = func(A1, A2, ...) (R1, R2, ...)` inside methods of a generic type (methods can't introduce new type params, and there are no tuple types). That caps how much compile-time type checking the DSL can provide. But we can do much better than pure `any`:

**Option A** (default) — reflect dispatch + runtime type checks:
```go
e.On("Alice").Returns("hi Alice")  // variadic any underneath
```
- Ergonomic for the common case.
- **Runtime type check at `Returns` call time**, not at test-body execution time. If you pass a wrong-typed return value, the test fails immediately when the rule is registered with a clear diagnostic including the expected and actual types.
- IDE sees real Go methods, no red squiggles. Autocomplete won't narrow `Returns(args ...any)` to the concrete return type, but autocomplete for `.On`, `.Match`, `.Returns`, `.Times`, etc. works normally.

**Option C (hybrid)** — layer typed helpers on top for the parts most likely to cause bugs:

```go
// .Match is fully typed because the predicate is a normal typed function.
// gopls infers `name string` from F.
e.Match(func(name string) bool { return strings.HasPrefix(name, "admin_") }).
  Returns("admin")

// .DoFunc takes a function with F's exact shape — full type checking.
e.On("Alice").DoFunc(func(name string) string {
    return "special " + name
})
```

`Match(predicate)` works because we use a generic free function or a constrained method — details in implementation.

**The recommended default surface** for v1:

| Want | Use | Type safety |
|---|---|---|
| Simple equality match + canned return | `.On(args).Returns(vals)` | runtime-checked at registration |
| Typed argument predicate | `.Match(func(args) bool)` | compile-time checked |
| Typed response function | `.DoFunc(func(args) returns)` | compile-time checked |
| Catch-all | `.OnAny().Returns(vals)` | runtime-checked |

## Verification model

Two levels of verification run at `t.Cleanup`:

**1. Per-rule bounds.** For each rule, compare actual call count against the rule's bound:
- Exact (`Times(n)`): fail if `count != n`.
- At-least (`AtLeast(n)`, strict default): fail if `count < n`.
- Never (`Never()`): fail if `count != 0`.
- Maybe/lenient (`.Maybe()`, `.OnAny()` default): never fails on count.

**2. Unmatched-call tracking.** Unmatched calls already fail at call time, but we also include a summary at cleanup so a failure cascade is easier to diagnose.

Both together give you "mock was actually called" (strict rules fail verification if nothing hit them) and "no unexpected calls snuck in" (unmatched calls fail immediately).

## Interactions with the rest of the rewire API

- **`rewire.Real(t, bar.Greet)`** — still works, returns the pre-rewrite real. Independent of whatever dispatcher `expect.For` installed. Useful for spy-style rules.
- **`rewire.Restore(t, bar.Greet)`** — early-clears the dispatcher. Subsequent calls go to the real. Unusual in combination with an active expectation — document as "supported but unusual; verification still runs at cleanup."
- **`rewire.Func(t, bar.Greet, ...)` alongside `expect.For(t, bar.Greet)`** — the second installation clobbers the first. Detect and `t.Fatal` with a clear message: "rewire/expect: target %s already has a mock installed". Same rule applies to calling `expect.For` twice on the same target in one test.
- **Generic functions / generic methods** — `expect.For(t, bar.Map[int, string])` or `expect.For(t, (*bar.Container[int]).Add)` work transparently, because `rewire.Func` already handles them and `expect.For` just delegates through.

## Implementation sketch

### New package: `pkg/rewire/expect`

```
pkg/rewire/expect/
├── expect.go        // For[F], *Expectation[F], *Rule[F], public API
├── dispatch.go      // reflect.MakeFunc dispatcher, call routing
├── match.go         // argument matching primitives
├── verify.go        // t.Cleanup verification
└── expect_test.go
```

### Types

```go
type Expectation[F any] struct {
    t               testing.TB
    name            string        // from runtime.FuncForPC
    fnType          reflect.Type  // reflect.TypeOf(original)
    mu              sync.Mutex
    rules           []*Rule[F]
    allowUnmatched  bool
}

type Rule[F any] struct {
    parent   *Expectation[F]
    matcher  matcher               // argument predicate
    response response              // return values or callback
    bound    bound                 // Times, AtLeast, Never, Maybe
    count    int                   // actual call count
    decl     string                // human-readable source location for error messages
}
```

### `For` constructor

```go
func For[F any](t testing.TB, original F) *Expectation[F] {
    t.Helper()
    fnType := reflect.TypeOf(original)
    if fnType == nil || fnType.Kind() != reflect.Func {
        t.Fatal("expect.For: target must be a function")
    }
    name := runtime.FuncForPC(reflect.ValueOf(original).Pointer()).Name()
    name = strings.ReplaceAll(name, "[...]", "")

    e := &Expectation[F]{
        t:      t,
        name:   name,
        fnType: fnType,
    }

    dispatcher := reflect.MakeFunc(fnType, e.dispatch).Interface().(F)
    rewire.Func(t.(*testing.T), original, dispatcher)
    t.Cleanup(e.verify)
    return e
}
```

Note: `t testing.TB` accepts both `*testing.T` and any fake implementing `testing.TB`, which matters for testing the DSL itself (catching `t.Errorf` without propagation). We type-assert to `*testing.T` when handing off to `rewire.Func` — there's some friction here, see Risks below.

### Dispatcher

```go
func (e *Expectation[F]) dispatch(args []reflect.Value) []reflect.Value {
    e.mu.Lock()
    defer e.mu.Unlock()
    for _, rule := range e.rules {
        if rule.matcher.match(args) {
            rule.count++
            return rule.response.produce(args, e.fnType)
        }
    }
    // Unmatched call
    if e.allowUnmatched {
        // Delegate to the real — but we don't have a direct reference.
        // Workaround: store the original function on Expectation[F] at
        // construction so we can call it here.
        return e.callReal(args)
    }
    e.t.Errorf("rewire/expect: unexpected call to %s with args %s", e.name, formatArgs(args))
    return zeroValues(e.fnType)
}
```

### Matchers

```go
type matcher interface {
    match(args []reflect.Value) bool
    describe() string // for error messages
}

type literalMatcher struct{ args []any }
type predicateMatcher struct{ fn reflect.Value } // pre-validated to have correct arg types
type anyMatcher struct{}
```

`literalMatcher` uses `reflect.DeepEqual`. `predicateMatcher` pre-validates at registration time that the predicate's arg types match `F`'s arg types. `anyMatcher` always matches.

### Rule-builder methods

```go
func (e *Expectation[F]) On(args ...any) *Rule[F] {
    // Validate arg count and types against e.fnType at registration time.
    // Append a new rule with literalMatcher and a strict default bound.
}

func (e *Expectation[F]) Match(predicate any) *Rule[F] {
    // Validate predicate is a func with matching arg types and bool return.
    // Append a new rule with predicateMatcher and a strict default bound.
}

func (e *Expectation[F]) OnAny() *Rule[F] {
    // Append a new rule with anyMatcher and a lenient default bound.
}

func (e *Expectation[F]) AllowUnmatched() *Expectation[F] {
    e.allowUnmatched = true
    return e
}
```

### Rule terminators

```go
func (r *Rule[F]) Returns(values ...any) *Rule[F] {
    // Validate value count and types against e.fnType's return types at
    // registration time. Store a valuesResponse.
}

func (r *Rule[F]) DoFunc(fn F) *Rule[F] {
    // Store a funcResponse. Fully type-checked because fn is of type F.
}
```

### Bound methods

```go
func (r *Rule[F]) Times(n int) *Rule[F]
func (r *Rule[F]) AtLeast(n int) *Rule[F]
func (r *Rule[F]) Never() *Rule[F]
func (r *Rule[F]) Maybe() *Rule[F]
```

### Verification

```go
func (e *Expectation[F]) verify() {
    e.t.Helper()
    e.mu.Lock()
    defer e.mu.Unlock()
    for i, rule := range e.rules {
        if err := rule.bound.check(rule.count); err != nil {
            e.t.Errorf("rewire/expect: expectation %s rule #%d (%s) %s",
                e.name, i, rule.matcher.describe(), err)
        }
    }
}
```

## Testing plan

- **Unit tests in `pkg/rewire/expect/`:**
  - Rule matching: literal, predicate, anyMatcher.
  - First-fit ordering: multiple rules with overlapping patterns.
  - Call counting: verify count increments and bounds check.
  - Type checking: wrong argument count, wrong arg types, wrong return types — all rejected at registration with clear messages.
  - Predicate validation: non-function, wrong arg types, wrong return type.
  - Unmatched calls: default fail, AllowUnmatched pass-through.
- **Self-testing DSL failures:** use a recording `testing.TB` fake that captures `Errorf` calls without propagating. Lets us assert "this misuse produces this exact error message."
- **End-to-end in `example/foo/`:**
  - `bar.Greet` with several rules and verification.
  - Methods via method expression.
  - Generic function (`bar.Map[int, string]`).
  - Generic method (`(*bar.Container[int]).Add`).
  - Interaction with `rewire.Real` for spy-style rules.
  - Verification failure path via subtest.

## Risks and open questions

- **`testing.T` vs `testing.TB`.** Public API takes `*testing.T` (consistent with the rest of rewire). For DSL self-testing we need a way to catch errors — either a recording fake or a subtest. Lean toward: public API is `*testing.T`, DSL internals use `testing.TB` so we can pass a fake in unit tests.
- **Reflect overhead.** Every call through the dispatcher does a reflect.Call. Should be fine for test-path code; measure if concern arises.
- **Mutex contention on hot loops.** The dispatcher locks for every call. For tests that invoke the mocked function in a tight loop from multiple goroutines, this could matter. Acceptable for v1.
- **Error messages should point at user code.** Ideally, when rule registration fails (wrong type), the error should point at the user's line, not the DSL internals. Use `t.Helper()` religiously.

## Effort estimate

- ~400-600 lines for the opt-in package (expect.go, dispatch.go, match.go, verify.go).
- ~300 lines of tests.
- No rewriter / toolexec / codegen changes.
- Docs: new page `docs/expectations.md`, brief link from `docs/function-mocking.md`.

## Exit criteria

- `expect.For(t, bar.Greet).On("Alice").Returns("hi").Times(1)` works end-to-end.
- Default strict verification catches "mock was never called" at cleanup time.
- Multiple rules with first-fit ordering for `.On(literal)`, `.Match(predicate)`, `.OnAny()`.
- Wrong argument/return type at registration fails fast with a clear message.
- Works for methods, generic functions, and generic methods with no special API.
- No IDE red squiggles — `gopls` resolves every identifier cleanly.
- Documented with a concise `docs/expectations.md` page.
