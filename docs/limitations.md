# Limitations

The compiler-intrinsic, parallel-mock, bodyless-function, and build-cache items below apply to compile-time function and method mocking (toolexec). Interface mock generation has its own (smaller) set of gaps — see [Interface mocks](#interface-mocks-rewirenewmockt) at the bottom of this page.

Per-instance method stubs are fully supported natively — see [Per-instance method mocks](method-mocking.md#per-instance-method-mocks).

## Compiler intrinsics

Functions like `math.Abs`, `math.Sqrt`, and `math.Floor` are replaced with CPU instructions by the Go compiler at the **call site**. Even though rewire rewrites the function body, callers bypass it entirely — the compiler emits hardware instructions (e.g., `FABS` on arm64) instead of a function call.

Rewire detects these automatically and fails with a clear error message. Use non-intrinsic alternatives where possible (e.g., `math.Pow` works fine).

## No parallel mock safety

Parallel tests in the same package should not mock the **same function** with different replacements. The mock variable is shared, so two parallel tests setting it will race.

Two parallel tests mocking **different** functions is fine — there's no contention.

!!! note
    This only matters for tests using `t.Parallel()`. Sequential tests (the default) don't have this issue since `t.Cleanup` restores the original between tests.

## Bodyless functions

Functions implemented in assembly (no Go body) cannot be rewritten. These are typically low-level runtime or math functions. Rewire will fail with an error if you try to mock one.

## Interface mocks (`rewire.NewMock[T]`)

The toolexec interface mock generator handles non-generic interfaces, generic interfaces with arbitrary type-argument shapes (builtins, slices, maps, pointers, external-package types, nested generic instantiations), and methods using imported types. The remaining gaps:

- **Embedded interfaces** — an interface that includes another interface (`io.ReadCloser` embeds `io.Reader` + `io.Closer`) is rejected with a clear error. Workaround: define the composed method set inline, or use the older `rewire mock` CLI.
- **Types from the interface's own declaring package** — an interface in `bar/` whose method signatures reference `*bar.Greeter` directly (rather than via the `bar.` qualifier) is rejected. The codegen needs to qualify bare identifiers with the declaring package alias before this works.

The CLI mock generator (`rewire mock` — the older `go:generate`-style path) has a different scope: it does NOT support generic interfaces but accepts most other shapes silently. If you need generic interface mocking, use `rewire.NewMock[T]` instead.

## Build cache considerations

Go's build cache keys on compilation inputs including the toolexec binary. If you change rewire versions, you may need to clean the cache:

```bash
go clean -cache
```

Using a separate test cache (`GOCACHE`) avoids conflicts between `go build` and `go test`. See [Setup](setup.md) for details.
