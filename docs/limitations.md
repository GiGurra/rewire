# Limitations

These limitations apply to compile-time function and method mocking (toolexec). Interface mock generation is not affected — if you need per-instance method stubs or any of the behaviors below, use [interface mocks](interface-mocks.md) instead.

## Compiler intrinsics

Functions like `math.Abs`, `math.Sqrt`, and `math.Floor` are replaced with CPU instructions by the Go compiler at the **call site**. Even though rewire rewrites the function body, callers bypass it entirely — the compiler emits hardware instructions (e.g., `FABS` on arm64) instead of a function call.

Rewire detects these automatically and fails with a clear error message. Use non-intrinsic alternatives where possible (e.g., `math.Pow` works fine).

## Method-level type parameters

Go 1.18+ does not allow methods to declare their own type parameters — any type parameters on a method come from its receiver type. Rewire follows the same rule: methods on generic types like `func (c *Container[T]) Add(v T)` are supported (see [Method Mocking](method-mocking.md#methods-on-generic-types)), but a hypothetical `func (c *C) Method[X any]()` would be rejected.

## No parallel mock safety

Parallel tests in the same package should not mock the **same function** with different replacements. The mock variable is shared, so two parallel tests setting it will race.

Two parallel tests mocking **different** functions is fine — there's no contention.

!!! note
    This only matters for tests using `t.Parallel()`. Sequential tests (the default) don't have this issue since `t.Cleanup` restores the original between tests.

## Bodyless functions

Functions implemented in assembly (no Go body) cannot be rewritten. These are typically low-level runtime or math functions. Rewire will fail with an error if you try to mock one.

## Build cache considerations

Go's build cache keys on compilation inputs including the toolexec binary. If you change rewire versions, you may need to clean the cache:

```bash
go clean -cache
```

Using a separate test cache (`GOCACHE`) avoids conflicts between `go build` and `go test`. See [Setup](setup.md) for details.
