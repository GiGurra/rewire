# Limitations

The items below apply to compile-time function and method mocking (toolexec). Interface mock generation (`rewire.NewMock[T]`) has no documented limitations at this time — see [Interface Mocks](interface-mocks.md) for the full feature set.

Per-instance method stubs are fully supported natively — see [Per-instance method mocks](method-mocking.md#per-instance-method-mocks).

## Compiler intrinsics

Functions like `math.Abs`, `math.Sqrt`, and `math.Floor` are replaced with CPU instructions by the Go compiler at the **call site**. Even though rewire rewrites the function body, callers bypass it entirely — the compiler emits hardware instructions (e.g., `FABS` on arm64) instead of a function call.

Rewire detects these automatically and fails with a clear error message. Use non-intrinsic alternatives where possible (e.g., `math.Pow` works fine).

## No parallel mock safety (but conflicts are detected)

Parallel tests in the same package cannot mock the **same target** with different replacements — the mock variable is shared, so two parallel installs would race.

Rewire **detects this at install time**. If a second test calls `rewire.Func` / `rewire.InstanceFunc` on a target another live test already mocks, it fails via `t.Fatalf` with a diagnostic that names the conflicting test and suggests fixes:

```
rewire: cannot install mock for pkg.Foo — already mocked by test "TestOther".
  Two different tests tried to install a mock on the same target concurrently.
  rewire.Func / rewire.InstanceFunc are not parallel-safe for the same target:
  a single shared mock variable backs every call site, so overlapping installs
  would race silently on that variable.
  Fixes:
    - Remove t.Parallel() from one of the tests, or
    - Mock a different target in each parallel test, or
    - Use rewire.InstanceFunc on separate receivers so each test owns its own scope.
```

What is **not** a conflict:

- Two parallel tests mocking **different** targets.
- Two parallel tests mocking the **same method on different receivers** via `rewire.InstanceFunc` — ownership is scoped per receiver address, so each test owns its own scope.
- A single test **reinstalling** a mock on a target it already owns (e.g., `rewire.Func(t, foo, mockA); rewire.Func(t, foo, mockB)` in the same test).

Ownership is released automatically when the owning test ends (via `t.Cleanup`). `rewire.RestoreFunc`, `rewire.RestoreInstance`, and `rewire.RestoreInstanceFunc` also release ownership eagerly, so a test that mocks and then restores mid-run frees the target for a concurrent test to claim.

!!! note
    This only triggers for tests using `t.Parallel()` or otherwise running concurrently. Sequential tests (the default) take and release ownership cleanly between tests.

### What detection cannot catch: mocker vs. non-mocker in parallel

The conflict detector only fires when **two tests call a rewire function** (`rewire.Func` / `rewire.InstanceFunc`) on the same target concurrently. It cannot detect the asymmetric case: one parallel test installs a mock, while another parallel test doesn't call any rewire function at all but happens to exercise the same target expecting the real implementation.

```go
func TestA(t *testing.T) {
    t.Parallel()
    rewire.Func(t, bar.Greet, func(string) string { return "fake" })
    // ... uses bar.Greet
}

func TestB(t *testing.T) {
    t.Parallel()
    // No rewire call here — just uses bar.Greet directly.
    if got := bar.Greet("x"); got != "Hello, x!" { // ← may silently see "fake"
        t.Fatalf("got %q", got)
    }
}
```

TestA legitimately claims the mock variable. TestB never calls rewire, so nothing on TestB's side can observe the ownership. If both run concurrently, `bar.Greet` in TestB is non-deterministically either the real function or TestA's mock, depending on scheduling. Outcomes are unstable run-to-run.

There's no reliable way for rewire to catch this from the outside — TestB has no rewire-instrumented entry point. The usual symptoms are flaky tests that pass in isolation and fail (or pass with the wrong output) under `-parallel`. If you see that pattern, audit whether another parallel test is mocking the same target.

**Goroutine-level parallel safety was investigated and not shipped.** A working end-to-end prototype exists on a draft PR: [#6 — Parallel-safe `rewire.Func` via goroutine-inherited pprof labels](https://github.com/GiGurra/rewire/pull/6). It replaces the shared `Mock_Foo` variable with a per-goroutine-tree dispatch table keyed on a `runtime/pprof` labels pointer, so parallel tests each see their own mock and child goroutines inherit automatically. Held back from merging because it relies on an unofficial linkname loophole and the new wrapper shape is too complex for Go's inliner to absorb (a real regression on the "rewire is free when not mocked" property). See [`plans/parallel_test_safety_findings.md`](https://github.com/GiGurra/rewire/blob/main/plans/parallel_test_safety_findings.md) for the full write-up.

The conflict detection documented above is the pragmatic alternative: it doesn't make rewire parallel-safe, but it turns a silent data race into a loud, actionable test failure.

## Bodyless functions

Functions implemented in assembly (no Go body) cannot be rewritten. These are typically low-level runtime or math functions. Rewire will fail with an error if you try to mock one.

## Build cache and new mock targets

When you add a new `rewire.Func` target for a package that was already cached (e.g., adding `rewire.Func(t, os.Hostname, ...)` when only `os.Getwd` was mocked before), the cached `.a` file for that package lacks the new `Mock_`/`Real_` variables. Rewire **detects this automatically** and clears the build cache, printing:

```
rewire: mock target set changed (affected packages: os, strings, ...).
    The build cache has been cleared automatically.
    Please re-run your test command — the next run will succeed.
```

**Why a re-run is needed:** rewriting `os` changes its linker fingerprint. Every package compiled against the old `os` (including `testing`, `fmt`, etc.) embeds that fingerprint. The linker rejects mismatches, so the entire transitive dependency tree must be rebuilt — equivalent to clearing the cache. This only happens when the set of mocked functions changes, not during normal TDD.

If you change rewire versions, you may also need to clean the cache manually:

```bash
go clean -cache
```

Using a separate test cache (`GOCACHE`) avoids conflicts between `go build` and `go test`. See [Setup](setup.md) for details.
