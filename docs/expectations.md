# Expectation DSL

A declarative alternative to writing closures with manual counters. Opt in via a separate package.

```go
import "github.com/GiGurra/rewire/pkg/rewire/expect"
```

If you don't import `expect`, you pay nothing — it's strictly additive on top of `rewire.Func` and the core rewire API is unchanged.

## When to use it

Plain `rewire.Func` is perfect for light mocking. You write a closure, capture state if you need it, done:

```go
rewire.Func(t, bar.Greet, func(name string) string {
    return "mocked"
})
```

The moment you need multiple patterns, call counts, predicate-based matching, or automatic "was this actually called?" verification, closures get tedious. That's what `expect` is for.

```go
e := expect.For(t, bar.Greet)
e.On("Alice").Returns("hi Alice")
e.On("Bob").Returns("hi Bob")
e.Match(func(name string) bool {
    return strings.HasPrefix(name, "admin_")
}).Returns("admin")
e.OnAny().Returns("hi other")
```

Each rule has its own matching logic and call-count bound, and cleanup automatically verifies everything at test end.

## How it works (behind the scenes)

`expect.For(t, target)` **is** the mocking step. You never call `rewire.Func(t, target, ...)` alongside it — that would install two mocks and the second would clobber the first. Under the hood:

1. `expect.For` builds an `Expectation[F]` state object holding the rule list.
2. It constructs a dispatcher via `reflect.MakeFunc` whose type matches `target`.
3. It calls `rewire.Func(t, target, dispatcher)` — the dispatcher is installed as the mock **right now**.
4. It registers a `t.Cleanup` that runs verification at test end.
5. It returns the `*Expectation[F]` so the caller can build rules.

From the moment `expect.For` returns, the target is mocked. Every subsequent `.On(...)`, `.Match(...)`, `.OnAny()` call appends to the rule list under a mutex, and the dispatcher re-reads the list on every call — so rules added later still apply to later calls.

## Matching patterns

### Literal equality — `.On(args...)`

Matches calls whose arguments are `reflect.DeepEqual` to the provided values. Argument count and types are validated against the target's signature at **registration time**, not at dispatch time — so a wrong-typed argument fails the test immediately with a clear message, not mysteriously during test execution.

```go
e.On("Alice").Returns("hi Alice")
e.On(42, "hello").Returns(true)   // for a func(int, string) bool target
```

### Typed predicate — `.Match(fn)`

Matches calls for which the predicate returns `true`. The predicate is a normal Go function with the target's argument types and a `bool` return — **fully type-checked by Go's normal type inference**. Your IDE will autocomplete inside the predicate body.

```go
e.Match(func(name string) bool {
    return strings.HasPrefix(name, "admin_")
}).Returns("admin")
```

For methods, the receiver is the first parameter:

```go
e := expect.For(t, (*bar.Greeter).Greet)
e.Match(func(g *bar.Greeter, name string) bool {
    return g.Prefix == "VIP" && name == "Alice"
}).Returns("special VIP greeting")
```

### Catch-all — `.OnAny()`

Matches every call. Use it as a fallback after more specific rules, or as a simple stubbing shortcut when you just want any call to produce a canned return.

```go
e.On("Alice").Returns("hi Alice")
e.On("Bob").Returns("hi Bob")
e.OnAny().Returns("hi other")   // fallback for everyone else
```

### First-fit ordering

Rules are walked in **declaration order**, first-fit wins. Declare narrow rules first, `.OnAny()` last:

```go
e.On("Alice").Returns("specific Alice")    // rule 0 — narrow
e.Match(func(n string) bool {              // rule 1 — broader (starts with A)
    return strings.HasPrefix(n, "A")
}).Returns("starts with A")
e.OnAny().Returns("any")                   // rule 2 — fallback

bar.Greet("Alice")  // → rule 0 → "specific Alice"
bar.Greet("Anne")   // → rule 1 → "starts with A"
bar.Greet("Bob")    // → rule 2 → "any"
```

## Responses

### Fixed return values — `.Returns(vals...)`

The return value count and types are validated against the target's signature at registration time.

```go
e.On("Alice").Returns("hi Alice")
// For multi-return:
e.OnAny().Returns("result", nil)   // for func(...) (string, error)
```

### Typed callback — `.DoFunc(fn)`

Runs arbitrary code, receives the real arguments, returns whatever you compute. The callback takes the target's exact signature — **fully type-checked by Go**, not `any`-typed. Use this for:

- Input-dependent return values
- Capturing arguments for assertion
- Delegating to the real implementation via `rewire.Real`
- Any logic that depends on what was actually passed in

```go
// Input-dependent return:
e.OnAny().DoFunc(func(x int) int {
    return x * 2
})

// Argument capture:
var seen []string
e.OnAny().DoFunc(func(name string) string {
    seen = append(seen, name)
    return "recorded"
})

// Spy pattern — delegate to real:
realGreet := rewire.Real(t, bar.Greet)
e.OnAny().DoFunc(func(name string) string {
    return realGreet(name) + " [spied]"
})
```

### Branching responses

Mix `.On`, `.Match`, and `.OnAny` freely in a single expectation:

```go
e := expect.For(t, bar.TinyAdd)

// Literal short-circuit
e.On(0, 0).Returns(0)

// Predicate-matched branch
e.Match(func(a, b int) bool {
    return a < 0 || b < 0
}).DoFunc(func(a, b int) int {
    return -1
})

// Catch-all with input-dependent logic
e.OnAny().DoFunc(func(a, b int) int {
    return a*1000 + b
})
```

## Call-count bounds

Every rule carries a call-count bound that `t.Cleanup` verifies. The defaults match what you usually want, with explicit overrides:

| Method | Meaning |
|---|---|
| `.Times(n)` | exactly `n` calls |
| `.AtLeast(n)` | `n` or more calls |
| `.Never()` | must not be called at all |
| `.Maybe()` | zero-or-more, opt out of strict default |

### Default strictness

| Rule kind | Default | Reasoning |
|---|---|---|
| `.On(args)` | `AtLeast(1)` — strict | "I expect this specific arg to be passed" |
| `.Match(predicate)` | `AtLeast(1)` — strict | "I expect a call satisfying this predicate" |
| `.OnAny()` | any count | it's a catch-all, zero matches is fine |

This gives you "**was the mock actually called?**" coverage **for free**. If you write `e.On("Alice").Returns("hi")` and nothing ever calls `bar.Greet("Alice")`, verification at cleanup fails with:

```
rewire/expect: github.com/example/bar.Greet rule #0 .On("Alice")
  (declared at my_test.go:42) was called 0 time(s), expected at least 1
```

Opt out of strict for rules that may or may not be reached:

```go
e.On("optional").Returns("hi").Maybe()
```

### Never

Useful for asserting that something must NOT happen:

```go
e := expect.For(t, bar.Greet)
e.On("forbidden").Never()
e.OnAny().Returns("fine")

// If any call with "forbidden" happens, the test fails at call time
// with a clear "matched but was declared .Never()" diagnostic.
```

## Async testing — `Wait(count, timeout)`

For tests that kick off goroutines which eventually call the mocked function, use `Wait` on the rule to block until the rule has matched a given count, with a timeout.

```go
e := expect.For(t, bar.Greet)
rule := e.OnAny().DoFunc(func(name string) string {
    return "async-" + name
})

// Fire off async work that eventually calls bar.Greet a few times.
go startBackgroundWorker()

// Block until the rule has matched 3 times, or fail after 2 seconds.
rule.Wait(3, 2*time.Second)

// After Wait returns, the test body can safely assert post-state.
```

Details:

- **Semantics**: blocks until `r.count >= n`. Returns the rule (for chaining, though chaining past a blocking call is unusual).
- **Timeout**: on deadline, the test is failed via `t.Errorf` with a diagnostic showing the rule, the expected count, and the actual count at deadline. The test continues (it's not `t.Fatalf`) so other assertions can still run.
- **Immediate return**: if the count is already satisfied when `Wait` is called, it returns without sleeping. Safe to use synchronously after calls that have already happened.
- **Implementation**: simple 10ms polling loop over the rule's live count. No extra signaling state or channels. Polling latency is invisible in test timings.
- **Thread safety**: the rule's count is always read under the expectation's mutex — same mutex the dispatcher holds when incrementing. Safe under concurrent calls from any number of goroutines.
- **Interaction with bounds**: `Wait` complements `.Times(n)` / `.AtLeast(n)`. You typically set both — the bound verifies at cleanup that the expected count was reached, and `Wait` synchronizes the test body to the actual async completion. If `Wait` times out, cleanup's bound check also fails; both errors surface.
- **Don't use with `.Never()`** — a Never rule is expected to never match, so `Wait` would always time out. Not meaningful.

### How other libraries handle this

- **Mockito (Java)** uses `verify(mock, timeout(2000)).method()` — a timeout-aware verifier that polls until the assertion passes or fails.
- **testify (Go)** has `assert.Eventually(t, cond, wait, tick)` — a general-purpose condition poller, not mock-specific.
- **gomock (Go)** has nothing built-in; users typically synchronize manually with `sync.WaitGroup` or channels inside a `DoAndReturn` callback.

Rewire's `Wait` fits the Mockito-style pattern but anchored on the rule object you already have, so there's no extra verifier object or global state.

## Unmatched calls

By default, calls that match no rule fail the test at call time:

```
rewire/expect: unexpected call to github.com/example/bar.Greet("Carol") — no rule matched
```

Either add an `.OnAny()` fallback or opt into pass-through behavior:

```go
// Pass through unmatched calls to the real implementation.
e := expect.For(t, bar.Greet).AllowUnmatched()
e.On("Alice").Returns("mocked")

bar.Greet("Alice")  // "mocked"
bar.Greet("Bob")    // real bar.Greet body runs
```

## Works for everything rewire supports

The DSL is a thin layer on top of `rewire.Func`, so **every target shape that `rewire.Func` supports works with `expect.For` with zero extra code**:

### Methods

```go
e := expect.For(t, (*bar.Greeter).Greet)
e.On(&bar.Greeter{Prefix: "Hi"}, "Alice").Returns("mocked Alice")
```

### Generic functions

```go
e := expect.For(t, bar.Map[int, string])
e.OnAny().DoFunc(func(in []int, f func(int) string) []string {
    return []string{"mocked"}
})
// Only the [int, string] instantiation is replaced;
// bar.Map[float64, bool] still runs the real body.
```

### Generic methods

```go
e := expect.For(t, (*bar.Container[int]).Add)
e.OnAny().DoFunc(func(c *bar.Container[int], v int) {
    audit = append(audit, v)
})
```

Per-instantiation dispatch for generics and global-per-type dispatch for methods work exactly as they do for `rewire.Func` directly — the DSL just wraps it.

## Interactions with the rest of rewire

- **`rewire.Real(t, target)`** — works normally, returns the real implementation independent of whatever dispatcher `expect.For` installed. Useful for spy-style rules that delegate to the real.
- **`rewire.Restore(t, target)`** — early-clears the dispatcher. Subsequent calls go to the real. Unusual when combined with an active expectation, but supported. Verification still runs at cleanup.
- **`rewire.Func(t, target, ...)` alongside `expect.For(t, target)`** — don't do this. The second install clobbers the first. `expect.For` **is** the mocking step; use it instead of `rewire.Func`, not alongside.

## Error reporting

The DSL reports errors via `t.Errorf` (not `t.Fatalf`), so multiple rule violations surface in a single test run. Error messages include:

- The target's canonical name
- The rule's index and matcher description (`.On("Alice")`, `.Match(func(string) bool)`, `.OnAny()`)
- The source location where the rule was declared (`my_test.go:42`)
- The actual vs expected call count or mismatch detail

Wrong argument count, wrong argument types, non-function predicates, wrong predicate return types, and type mismatches on `.Returns` values are all validated at **registration time** — the test fails immediately when the invalid rule is declared, not mysteriously during the test body.

## Limitations

- **Full argument/return type checking at compile time isn't possible** — Go's generics can't decompose a function type `F = func(A1, A2) R` into its parts inside methods of a generic type. We do the next best thing: `.On(args...)` and `.Returns(vals...)` take `...any`, but every argument and return value is type-checked against the target's reflect signature at registration time. Wrong types fail immediately with a clear diagnostic, not later at test runtime.
- **`.Match(predicate)` and `.DoFunc(fn)` are fully type-checked at compile time** — both take typed Go functions whose types are enforced by the Go compiler. Prefer these when you want compile-time argument type safety.
- **Parallel test safety** — inherited from `rewire.Func`. Don't use `expect.For` on the same target in `t.Parallel()` tests that run concurrently; they'll race on the package-level mock variable.
- **Reflect dispatch overhead** — each mocked call goes through `reflect.Call`. Should be negligible for test-path code.

## Related

- [Function Mocking](function-mocking.md) — the underlying `rewire.Func` API.
- [Method Mocking](method-mocking.md) — method expression syntax used by `expect.For`.
- [Interface Mocks](interface-mocks.md) — alternative API via code generation, useful for per-instance mocking.
