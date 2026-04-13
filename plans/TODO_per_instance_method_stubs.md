# TODO — Per-instance method stubs

**Status:** not started
**Original survey item:** #1
**Layer:** rewriter + `pkg/rewire` (new API).

## Motivation

Today, `rewire.Func(t, (*bar.Server).Handle, mock)` replaces `Handle` for **every** `*Server` instance in the test. You can branch on receiver identity inside the closure to get per-instance behavior, but it's manual and reads poorly.

gomock / testify-mock users expect `mock1.Handle` to behave differently from `mock2.Handle` without any closure gymnastics. Interface mocks (`rewire mock`) already cover this use case via DI, but that's a different API and forces you to design your code around interfaces. Per-instance toolexec-level mocking would keep rewire's "no DI plumbing" promise while supporting instance-specific behavior.

## API design

New function: `rewire.FuncOn`.

```go
s1 := &bar.Server{Name: "primary"}
s2 := &bar.Server{Name: "secondary"}

rewire.FuncOn(t, s1, (*bar.Server).Handle, func(s *bar.Server, req string) string {
    return "primary-mock: " + req
})

// s2.Handle still runs the real body.
// A separate FuncOn can mock s2 differently.
rewire.FuncOn(t, s2, (*bar.Server).Handle, func(s *bar.Server, req string) string {
    return "secondary-mock: " + req
})

// Any *bar.Server not covered by FuncOn falls through to... the global mock
// (if rewire.Func was also called) or the real implementation.
```

Dispatch order inside the rewritten wrapper:
1. Per-instance mock for this receiver, if one exists.
2. Package-global `Mock_Type_Method`, if set (existing rewire.Func).
3. The real implementation.

`rewire.Restore` semantics — we need to think about this. Probably: `rewire.Restore(t, (*bar.Server).Handle)` clears the global mock. A new `rewire.RestoreOn(t, s1, (*bar.Server).Handle)` clears just the per-instance entry. `t.Cleanup` handles the usual restore.

## Scope

- **Pointer receivers only.** Value receivers get copied, so there's no stable identity to key a per-instance mock on. Rewriter rejects `rewire.FuncOn(t, s, bar.Point.String, ...)` with a clear error.
- **Methods only.** Plain free functions don't have a receiver, so per-instance doesn't apply. `FuncOn` is method-only by design.
- **Works for generic methods too.** `rewire.FuncOn(t, container, (*bar.Container[int]).Add, mock)` — the sync.Map is keyed on the receiver pointer, which works regardless of generic type args.

## Implementation sketch

### 1. Rewriter

Emit an additional per-method sync.Map:

```go
var Mock_Server_Handle          func(*Server, string) string  // global (existing)
var Mock_Server_Handle_ByInstance sync.Map                    // new: map[*Server]func(...)
```

And extend the wrapper body to check per-instance first:

```go
func (s *Server) Handle(req string) string {
    if m, ok := Mock_Server_Handle_ByInstance.Load(s); ok {
        return m.(func(*Server, string) string)(s, req)
    }
    if _rewire_mock := Mock_Server_Handle; _rewire_mock != nil {
        return _rewire_mock(s, req)
    }
    return s._real_Server_Handle(req)
}
```

Only emit the `_ByInstance` sync.Map for **pointer-receiver methods**. Value receivers get the existing shape unchanged.

For **generic methods**, the `_ByInstance` sync.Map coexists with the existing `Mock_X sync.Map` keyed on type signature. Dispatch order: per-instance → per-type-signature (global for that instantiation) → real.

### 2. `pkg/rewire/replace.go`

Add `FuncOn`:

```go
func FuncOn[Instance any, F any](t *testing.T, instance Instance, original F, replacement F) {
    t.Helper()

    // validateFuncArgument on original (same as rewire.Func)
    // methodValueError check
    // resolveMockVar to find the per-instance sync.Map

    instancePtr := extractReceiverPointer(instance)
    if instancePtr == 0 {
        t.Fatal("rewire.FuncOn: instance must be addressable (pointer-receiver methods only)")
    }

    byInstance := resolveByInstanceMap(t, original)
    byInstance.Store(instancePtr, replacement)
    t.Cleanup(func() { byInstance.Delete(instancePtr) })
}
```

Registry changes:
- New registry: `byInstanceRegistry sync.Map` — maps `canonical method name` → `*sync.Map` (the per-method `_ByInstance` table).
- Codegen: in `generateRegistration`, for pointer-receiver methods, emit a `rewire.RegisterByInstance(name, &pkg.Mock_Type_Method_ByInstance)` call alongside the existing `Register`.
- `resolveByInstanceMap(t, original)` fetches it from the registry and fatals cleanly if the target isn't a pointer-receiver method.

### 3. `parseTargetName` / `isPointerReceiverMethod`

The codegen currently uses `parseTargetName` to split a target name into `typeName / methodName / isMethod`. Extend with `isPointer` so we only emit `_ByInstance` registration for pointer-receiver targets:

```go
func parseTargetName(name string) (typeName, methodName string, isMethod, isPointer bool)
```

### 4. Receiver-pointer extraction

The tricky runtime bit. Given a generic `Instance any` parameter holding `*bar.Server`, we need a stable identity (`uintptr`) to key the sync.Map.

```go
func extractReceiverPointer[I any](instance I) uintptr {
    v := reflect.ValueOf(instance)
    if v.Kind() != reflect.Pointer || v.IsNil() {
        return 0
    }
    return v.Pointer()
}
```

Open question: should the sync.Map key be `uintptr` or `any` (holding the actual pointer value)? `uintptr` is cheaper but doesn't prevent GC of the target. `any` containing the pointer keeps the target alive until `t.Cleanup` removes the entry. The latter is safer — if a test accidentally drops its last strong reference, the mock still fires correctly.

## Testing plan

- **Unit tests in the rewriter**: assert that pointer-receiver methods emit both `Mock_X` and `Mock_X_ByInstance` with the correct wrapper body, and that value-receiver methods don't emit `_ByInstance` at all.
- **End-to-end tests in `example/foo/per_instance_test.go`**:
  - Two instances of `*bar.Greeter`, mock one via `FuncOn`, verify only that instance sees the mock.
  - Per-instance + global combined (`FuncOn` for s1, `Func` for everyone else): s1 gets its specific mock, all other instances get the global, no instance falls through to the real.
  - Generic type: per-instance mock on `(*bar.Container[int]).Add`, verify scoping.
  - Restore semantics: `RestoreOn(t, s1, ...)` clears only s1's mock while the global stays in place.
- **Verify inlining behavior** for the new wrapper shape. The extra `sync.Map.Load` branch might cost us inlineability for tiny leaf methods. Update `scripts/check-inlining.sh` if the budget is still fine, or accept a small regression and document it.

## Edge cases and caveats

- **GC pinning.** Per-instance entries hold a reference to the receiver until `t.Cleanup` removes them. This is usually fine for tests, but watch out for tests that allocate large objects and expect GC before test end.
- **Pointer identity vs interface identity.** If the user mocks via an interface (`var s bar.Doer = &impl{}; rewire.FuncOn(t, s, ...)`), `reflect.ValueOf(s).Pointer()` returns the address of the concrete impl, which is what we want. Test this path.
- **Sync.Map overhead on non-mocked hot paths.** For every method call, we now do a `sync.Map.Load` before the usual nil check. sync.Map is optimized for mostly-read workloads, so the empty-map path is cheap, but measure before shipping. If it's a problem, add an atomic "has any entries" flag as a fast-path guard.
- **Parallel test safety.** Same global-variable concern as today — `t.Parallel()` tests that set up different per-instance mocks for the same function will still race on the `_ByInstance` sync.Map internal state. sync.Map handles concurrent access safely but doesn't give us test isolation. Document this.

## Effort estimate

- Rewriter: ~100 lines — add the `_ByInstance` emission path in the method rewrite branch, extend the wrapper body template.
- Scanner: no change. The existing `(*Type).Method` extraction already covers the name form.
- Codegen: ~30 lines — emit `RegisterByInstance` for pointer-receiver methods.
- `pkg/rewire`: ~80 lines — `FuncOn`, `RestoreOn`, `RegisterByInstance`, `resolveByInstanceMap`, `extractReceiverPointer`.
- Tests: ~150 lines rewriter unit + ~150 lines end-to-end.
- Docs: new section in `docs/method-mocking.md`, brief note in `README.md`.

## Exit criteria

- `FuncOn(t, instance, (*Type).Method, mock)` replaces `Method` only for `instance`; other instances use the real implementation.
- `FuncOn` + `Func` combined: per-instance overrides global, global overrides real.
- `RestoreOn(t, instance, ...)` clears the per-instance mock without touching the global.
- Works for generic methods.
- Clear error when used on a value receiver method or a plain function.
- Inlining script still passes (or regression is documented).
