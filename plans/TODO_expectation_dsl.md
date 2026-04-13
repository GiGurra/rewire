# TODO — Expectation DSL (opt-in)

**Status:** not started
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

- **Opt-in.** Lives in a separate package (proposed: `pkg/rewire/expect`). Users who don't import it see no new API surface and pay no runtime cost.
- **Layer, not a rewrite.** Built on top of `rewire.Func` + reflect. No changes to the rewriter, scanner, toolexec, or codegen.
- **Minimal first.** Ship a small useful subset, see what people actually ask for, expand from there. Resist the temptation to build a full gomock-equivalent up front.

## Proposed API

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
    e.OnAny().Returns("hi other")   // fallback

    // ... code under test exercises bar.Greet ...

    // Automatic verification via t.Cleanup:
    //   - any Times() / AtLeast() bounds are checked
    //   - unmatched inputs (no .OnAny()) fail the test
}
```

Other patterns to support in v1:

```go
// Call counting
e.On("Alice").Returns("hi").Times(1)
e.On("Bob").Returns("hi").AtLeast(1)

// Side effects
e.OnAny().Do(func(name string) string {
    calls = append(calls, name)
    return "recorded"
})

// Predicate-based matching
e.Match(func(name string) bool {
    return strings.HasPrefix(name, "admin_")
}).Returns("admin greeting")
```

**Explicitly NOT in v1:**
- `gomock.InOrder(...)` call-ordering graphs
- `gomock.Any()` / `gomock.Eq(x)` / `gomock.Not(m)` matcher algebra (use `.Match(predicate)` instead)
- Mocks across multiple functions tied by a single expectation object
- Verification hooks outside t.Cleanup

## Implementation sketch

### One new package: `pkg/rewire/expect`

```
pkg/rewire/expect/
├── expect.go     // For[F any], Expectation[F] type
├── match.go      // matching / dispatch state machine
└── expect_test.go
```

### `For[F any](t *testing.T, original F) *Expectation[F]`

At construction:
1. Allocate a new `Expectation[F]` state object (holds the rule list, call counts, etc.).
2. Build a dispatcher closure via `reflect.MakeFunc` whose type is `reflect.TypeOf(original)`. The closure's body reads the current rule list, finds the first matching rule, increments its call count, and returns the rule's result (or invokes the rule's `Do` func).
3. Call `rewire.Func(t, original, dispatcher)` to install it.
4. Register `t.Cleanup` that runs verification (missed expectations, unmatched inputs).

### The dispatcher

Since `F` is generic but `reflect.MakeFunc` works on `reflect.Type`, we construct the dispatcher in terms of `[]reflect.Value`:

```go
fnType := reflect.TypeOf(original)
dispatcher := reflect.MakeFunc(fnType, func(args []reflect.Value) []reflect.Value {
    e.mu.Lock()
    defer e.mu.Unlock()
    rule := e.findMatch(args)
    if rule == nil {
        e.t.Errorf("rewire/expect: unexpected call to %s with args %v", e.name, args)
        return zeroValues(fnType)
    }
    rule.count++
    return rule.produce(args)
}).Interface().(F)

rewire.Func(t, original, dispatcher)
```

### Rule matching

Each rule records:
- An argument predicate (from `.On(args...)` literal-equality or `.Match(fn)` predicate or `.OnAny()` wildcard).
- A response: a typed return value list or a `Do` func that computes the response.
- A call count and optional bound (`Times(n)`, `AtLeast(n)`).

Matching is first-fit in declaration order. This is predictable and keeps the implementation simple.

### Verification

On `t.Cleanup`:
- For each rule with a `Times(n)` bound: fail if `count != n`.
- For each rule with an `AtLeast(n)` bound: fail if `count < n`.
- Rules without bounds are accepted at any count (including zero).

### Argument-matching helpers

Ship with just two matching primitives:
- `On(args...)` — literal equality via `reflect.DeepEqual`. Variadic to match any function arity.
- `Match(pred any)` — `pred` is a function of the same argument types as the target, returning `bool`. Invoked via reflect.

## Open design questions

1. **Literal `On(args...)` type safety.** Since `expect.For[F]` knows `F`, the ideal API would be `e.On[func(string) string]("Alice").Returns(...)`. But Go generics don't let a method on a generic type introduce new type params, so we'd need reflect-based type checking at runtime. Alternative: `e.On("Alice")` takes `...any`, and the return-value type is checked at runtime. Lose some compile-time safety, gain ergonomics.

2. **Calling the real function from within a rule.** Mockito's `.thenCallRealMethod()`. We already have `rewire.Real`, so the rule's `.Do(func(...) ...)` implementation can call it. Document the pattern.

3. **Strict vs lenient unmatched calls.** Default: unmatched calls fail the test (`t.Errorf`). Add `.AllowUnmatched()` escape hatch that passes through to the real function.

4. **Interaction with `rewire.Real` / `rewire.Restore`.** Because `expect.For` installs a dispatcher via `rewire.Func`, calling `rewire.Restore` on the same target should cleanly release the expectation. Test this in the test suite.

## Effort estimate

- ~300-500 lines for the opt-in package
- ~200 lines of tests
- No rewriter / toolexec / codegen changes
- Docs: new page `docs/expectations.md`, brief link from `docs/function-mocking.md`

## Testing plan

- Unit tests in `pkg/rewire/expect/`: rule matching, call counting, verification logic, argument predicates.
- End-to-end tests in `example/foo/`: exercise against `bar.Greet` and a method mock. Verify that `Times(n)` violations actually fail the test. (Use `t.Run` + a custom TB recorder, or integration via output parsing.)
- Reflect-dispatch tests: functions with mixed arg types, variadic params, multiple return values, error returns.

## Risks

- Testing "does this test fail correctly?" requires catching `t.Errorf` on a parent test without propagating. The standard workaround is a fake `testing.TB` implementation via an interface — but `*testing.T` is concrete. Options: use `t.Run` subtest and check the return value (cleanly isolated), or make the dispatcher call a `testing.TB` interface so tests can substitute a recorder.
- Reflect-based call dispatch has modest runtime cost. Should be negligible for test-path code but worth measuring.

## Exit criteria

- `expect.For(t, bar.Greet).On("Alice").Returns("hi").Times(1)` works and cleanup fails the test if `bar.Greet` wasn't called exactly once with `"Alice"`.
- Works for methods (same API via method expressions).
- Works for generic functions and generic methods (same API — it's all reflect underneath).
- Documented with a concise `docs/expectations.md` page.
