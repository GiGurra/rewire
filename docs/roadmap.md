# Roadmap

This page tracks known gaps compared to traditional Go mocking libraries (gomock, testify/mock, mockery, etc.) and what we plan to do about them.

Rewire targets the 80% case: stubbing a stdlib or third-party function without redesigning your code around interfaces. The items below are real gaps where rewire is weaker than interface-based libraries, roughly ordered by how often they bite real users.

## Planned work

### 1. Verify behavior under aggressive compiler inlining

**Status:** ✅ verified.

The rewrite transformation inserts a wrapper function that checks a package-level `Mock_` variable before calling `_real_<Name>`. We verified empirically (see `example/foo/inlining_test.go` and `scripts/check-inlining.sh`) that:

- The Go inliner **does** inline the rewritten wrapper into callers.
- The inliner **also** inlines `_real_<Name>` into the wrapper.
- The net result at every inlined call site is the full unrolled form — `if Mock_X != nil { return Mock_X(args) }; <real body>` — so the mock check fires at each call site, and the fast path is the original body with only a nil-check of overhead.
- `rewire.Real` still returns a usable reference: the exported `Real_<Name>` variable holds a function pointer to `_real_<Name>` taken at package init, even when `_real_<Name>` is also inlined elsewhere.

A dedicated script (`scripts/check-inlining.sh`) runs `go build -gcflags=-m=2` over `example/foo/` and asserts that the expected inlining decisions appear. It runs in CI so a future compiler or rewriter change can't silently regress this.

Not exhaustively verified: PGO-guided inlining and functions marked `//go:noinline` (the former should be fine by the same reasoning; the latter is irrelevant since rewire doesn't add that pragma).

### 2. Expectation DSL / argument matchers

**Status:** ✅ shipped as opt-in `pkg/rewire/expect` package.

Rewire's core API still gives you a closure-based style, but `import "github.com/GiGurra/rewire/pkg/rewire/expect"` opts you into a declarative rule-builder on top of `rewire.Func`:

```go
e := expect.For(t, bar.Greet)
e.On("Alice").Returns("hi Alice")
e.On("Bob").Returns("hi Bob")
e.Match(func(n string) bool {
    return strings.HasPrefix(n, "admin_")
}).Returns("admin")
e.OnAny().Returns("hi other")
```

Features:

- **Multiple patterns** — `.On(literals)`, `.Match(typed predicate)`, `.OnAny()` fallback. First-fit dispatch in declaration order.
- **Responses** — `.Returns(vals...)` for fixed values, `.DoFunc(fn)` for typed callbacks that compute input-dependent returns.
- **Call-count bounds** — `.Times(n)`, `.AtLeast(n)`, `.Never()`, `.Maybe()`. Strict default for `.On` and `.Match` (must match ≥ 1 call) gives automatic "was the mock actually called?" verification for free.
- **Automatic verification** — `t.Cleanup` checks every rule's bound and reports violations with source locations.
- **Works for methods, generic functions, and generic methods** — same API, because `expect.For` delegates to `rewire.Func` underneath.
- **No IDE red squiggles** — everything lives in a normal Go package that `gopls` resolves like any other.
- **Opt-in** — users who don't import the package pay nothing.

See [Expectations DSL](expectations.md) for the full story.

### 3. Generic functions

**Status:** ✅ supported (plain functions).

Generic functions work with the same `rewire.Func` / `rewire.Real` / `rewire.Restore` API as non-generic targets. Each type-argument combination is mocked independently:

```go
rewire.Func(t, bar.Map[int, string], func(in []int, f func(int) string) []string {
    return []string{"mocked"}
})
// bar.Map[float64, bool] still runs the real implementation
```

**How it works:**

- The rewriter emits a `sync.Map`-backed mock variable instead of a plain function var, keyed on `reflect.TypeOf(Map[T, U]).String()`. The self-reference inside the generic body produces the concrete instantiation's signature, which matches what the test side computes from the argument function value.
- For `rewire.Real`, the rewriter emits an exported `Real_X[T...]` generic delegating function. The toolexec scanner collects the specific type-argument combinations referenced in test files, and the codegen emits one `rewire.RegisterReal(...)` call per unique instantiation, materializing each concrete `Real_X[T1, T2]` at compile time. At runtime `rewire.Real` looks up the right entry via a composite `name + typeKey` registry key.
- `runtime.FuncForPC` reports `pkg.Map[...]` (with a literal `[...]`) for every instantiation, so there's a single canonical name per generic function that the registry lookup uses.

Methods on generic types are also supported — `(*bar.Container[int]).Add` is mockable just like `bar.Map[int, string]`, with each type-argument combination dispatched independently. See [Method Mocking → Methods on generic types](method-mocking.md#methods-on-generic-types).

**What's not supported:**

- Method-level type parameters (`func (c *C) Method[X any]()`). Go 1.18+ forbids them anyway.
- Runtime generic instantiation. Go doesn't allow it; rewire relies on the scanner seeing every instantiation that tests will use at compile time. If a test references `bar.Map[int, string]` without rewire seeing it, `rewire.Real(t, bar.Map[int, string])` will fail with "no real registered" — but this is equivalent to never calling rewire.Func on that instantiation.

## Bigger gaps we're not tackling (yet)

These are more fundamental than the items above. We're listing them so expectations are clear, not because we're actively working on them.

- **Per-instance method stubs.** Rewire's method mocks are type-global: mocking `(*Server).Handle` replaces Handle for every `*Server`. You can branch on receiver identity inside the closure, but it's manual. Fundamental to the package-level-variable design.
- **Parallel test safety.** `t.Parallel()` tests that mock the same function will race on the shared `Mock_` var. Unavoidable without per-goroutine state.
- **Compiler intrinsics and assembly stubs.** `math.Abs`, `math.Sqrt`, low-level runtime internals. Rewire detects and rejects these. The escape hatch is "wrap it in your own thin function and mock the wrapper."
- **Unexported functions across packages.** `bar.greet` (lowercase) can only be referenced from inside package `bar`, so the `rewire.Func` call has to live in `bar`'s own `_test.go` file. Doable but breaks the usual "test behavior from outside the package" flow.

## Non-goals

These are deliberate — we're not planning to add them because they'd conflict with rewire's design:

- Replacing rewire's globals with per-instance state. The whole point of the toolexec approach is that you don't need to plumb mocks through dependency injection.
- A full Mockito-equivalent DSL. If your tests need that level of structure, interface-based libraries are the better fit and rewire's interface mock codegen (`rewire mock`) is there for exactly that case.
