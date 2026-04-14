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

- **Dot imports in the interface's declaring file** — `import . "pkg"` brings package-level names into the file's top-level scope unqualified. The generator assumes any bare identifier in a method signature that isn't a predeclared type or a type parameter is a same-package type, which is wrong under a dot import. Dot imports are rare and discouraged; if you hit this, the generated file will fail to compile with a clear "undefined" error.
- **Module-aware package resolution** — rewire resolves an interface's declaring package via `go/build.Import`, which doesn't respect `replace` directives in `go.mod`, workspace files, or vendor directories. Interfaces in packages reachable via standard `GOPATH`/module-mode resolution work; the less common cases don't yet.

For the full list of supported shapes see [Interface Mocks](interface-mocks.md).

## Build cache considerations

Go's build cache keys on compilation inputs including the toolexec binary. If you change rewire versions, you may need to clean the cache:

```bash
go clean -cache
```

Using a separate test cache (`GOCACHE`) avoids conflicts between `go build` and `go test`. See [Setup](setup.md) for details.
