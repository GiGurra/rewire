# TODO — Mock unexported functions across package boundaries

**Status:** not started
**Original survey item:** #5
**Layer:** scanner + `pkg/rewire` (new API). No rewriter change.

## Motivation

Today, if you want to mock `bar.greet` (lowercase, unexported) from outside the `bar` package, you can't: Go's visibility rules forbid naming `bar.greet` from another package, so `rewire.Func(t, bar.greet, mock)` is a compile error. The only workaround is to put the test file inside `bar`'s own test directory — which defeats "test from outside the package" setups.

Rewire's rewriter is perfectly capable of rewriting unexported functions; the rewrite happens at compile time per-package and doesn't care about exportedness. The bottleneck is the user-facing API: there's no way for a test in package `foo` to *name* `bar.greet` in source code.

## API design

New function: `rewire.FuncByName`.

```go
// In foo_test.go — test lives in package foo, mocks an unexported function in bar.
rewire.FuncByName(t,
    "github.com/example/bar.greet",
    func(name string) string {
        return "mocked: " + name
    },
)
```

Signature:

```go
func FuncByName(t *testing.T, fullyQualifiedName string, replacement any)
```

Key points:
- `fullyQualifiedName` uses the exact format `runtime.FuncForPC` produces: `<importPath>.<funcName>` for free functions, `<importPath>.(*Type).Method` for pointer-receiver methods, `<importPath>.Type.Method` for value-receiver methods.
- `replacement` is `any` rather than a generic `F`, because Go's visibility rules prevent us from having a typed reference to `bar.greet` at the call site. The replacement is invoked via reflect.
- Signature is verified at runtime: if `replacement`'s type doesn't match the registered mock variable's type, `t.Fatal` with a clear diagnostic. This is the ergonomic downgrade — compile-time type errors become runtime errors.

Companion calls:

```go
// Read the real impl for spy-style delegation. Returns any; the caller
// type-asserts or invokes via reflect.
realFn := rewire.RealByName(t, "github.com/example/bar.greet")

// Early restore.
rewire.RestoreByName(t, "github.com/example/bar.greet")
```

## Scope

- **Unexported free functions**: yes. The primary use case.
- **Unexported methods**: yes. Same mechanism, the name just has the method syntax.
- **Generic unexported functions**: yes, but tricky — the user needs to spell out an instantiation somehow. Deferred; see open questions below.
- **Methods on generic types whose type is unexported**: same caveat. Deferred.

## Implementation sketch

### 1. Scanner (`internal/toolexec/scan.go`)

The scanner currently looks for `rewire.Func(t, <expr>, ...)` and uses AST inspection on `<expr>` to extract the target. For `FuncByName`, it needs to:
- Recognize `rewire.FuncByName(t, "literal string", ...)` calls.
- Treat the string literal as the canonical target name directly.
- Parse the string into `(importPath, funcName)` — everything up to the last dot before a method syntax is the import path; the rest is the target name in the canonical form.

Extend `extractMockTarget` (or add a sibling) so the `ast.Inspect` walker emits a target when `sel.Sel.Name` is `"FuncByName"` / `"RealByName"` / `"RestoreByName"` and `call.Args[1]` is an `*ast.BasicLit` with `Kind: token.STRING`.

Parsing the string: strip the surrounding quotes, then split. The rewriter already handles `Type.Method` / `(*Type).Method` forms, so we just need to separate `importPath` from the rest.

```go
// Example: "github.com/example/bar.(*Server).Handle"
//   importPath = "github.com/example/bar"
//   targetName = "(*Server).Handle"
//
// Example: "github.com/example/bar.greet"
//   importPath = "github.com/example/bar"
//   targetName = "greet"
```

The separator is "the last `.` that isn't inside method syntax". Concretely: scan from the end, skip any `).Method` suffix first, then find the last `.` in the remaining prefix. Small helper function.

Generic type args (`[...]`) should never appear in user-supplied strings — if they do, reject with a clear error.

### 2. Codegen (`generateRegistration`)

For `FuncByName` / `RealByName` / `RestoreByName` targets, the codegen still needs to emit `rewire.Register("<name>", &pkg.Mock_X)` and `rewire.RegisterReal("<name>", pkg.Real_X)` — **but** the test package can't import the target by its Go import path if it contains unexported types (it can import the package, just can't name unexported symbols from it). That's fine: the codegen emits Go code that references `pkg.Mock_X` (the `Mock_` vars are always exported by the rewriter, regardless of whether the original was), so the generated registration file compiles.

The one failure mode: if the user tries `FuncByName` on a function that doesn't exist in the target package, the generated `&pkg.Mock_nonexistent` reference fails to compile with a Go error. That's actually a reasonable failure mode — the user sees a clear compile error pointing at the registration file.

Wait — there's a subtlety. The codegen currently derives `Mock_` var names from the canonical target name via `mockVarName`. For `greet` (unexported), it'd generate `Mock_greet`. But the rewriter always emits `Mock_<exact name>`, preserving case. So `Mock_greet` (lowercase M on the prefix? no, capital M) — the rewriter uses `"Mock_" + funcName`, so `Mock_greet`. That's a valid Go identifier, starts with M (uppercase), so it's exported. Good. The generated registration call `rewire.Register("bar.greet", &bar.Mock_greet)` compiles cleanly.

Check the interaction with the `isGenericFunc` detection path, which already parses the target package source to decide whether a target is generic. That logic still works for unexported functions.

### 3. `pkg/rewire/replace.go`

Three new functions:

```go
// FuncByName is the string-addressed variant of Func. Use it to mock
// unexported functions from outside their defining package, or in any
// situation where you can't form a typed reference to the target.
// The replacement's type must match the target's signature; this is
// verified at runtime via reflect.
func FuncByName(t *testing.T, fullyQualifiedName string, replacement any) {
    t.Helper()

    mockPtrAny, ok := registry.Load(fullyQualifiedName)
    if !ok {
        // same error as Func's "function not found"
        ...
    }

    elemVal := reflect.ValueOf(mockPtrAny).Elem()

    // Verify replacement type matches the mock var type.
    replVal := reflect.ValueOf(replacement)
    if !replVal.IsValid() || replVal.Kind() != reflect.Func {
        t.Fatal("rewire.FuncByName: replacement must be a function")
    }
    if replVal.Type() != elemVal.Type() {
        t.Fatalf("rewire.FuncByName: replacement type %s does not match target %s (%s)",
            replVal.Type(), fullyQualifiedName, elemVal.Type())
    }

    // Same save/restore dance as Func.
    oldVal := reflect.New(elemVal.Type()).Elem()
    oldVal.Set(elemVal)
    elemVal.Set(replVal)
    t.Cleanup(func() { elemVal.Set(oldVal) })
}

func RealByName(t *testing.T, fullyQualifiedName string) any {
    // reuses the composite-key lookup from Real, but the "type key" is
    // derivable from the Mock_ var's type — one per function, since
    // the string-based API doesn't support generic instantiations
    // (no type argument syntax in the string).
    ...
}

func RestoreByName(t *testing.T, fullyQualifiedName string) {
    ...
}
```

The registry lookup reuses the same `sync.Map` used by `Func`/`Real`/`Restore`. The difference is that `FuncByName` skips the `FuncForPC` step — it trusts the user-supplied string.

## Open questions

1. **Generic unexported functions.** The string-based API has no natural way to spell `bar.greet[int, string]`. Options:
   a) Deferred: document that `FuncByName` doesn't support generics in v1.
   b) Add `FuncByNameFor(t, "bar.greet", typeArgs []reflect.Type, replacement)` — explicit type arg list. More verbose but precise.
   c) Let the string include brackets: `FuncByName(t, "bar.greet[int, string]", ...)` and parse them. Scanner extracts the type args from the string literal. This is the most symmetric with the typed API but requires string-parsing generics syntax.

   v1 recommendation: defer. Ship unexported non-generic support first.

2. **Refusing already-exported targets.** Should `FuncByName(t, "bar.Greet", mock)` work (where `Greet` is exported)? I'd say yes — it's just a string-addressed version of the existing API, and it's useful as an escape hatch for code generators that emit rewire calls. Document that the typed `rewire.Func` is preferred when possible.

3. **Auto-detection of `FuncByName` in the scanner.** Currently the scanner looks for `rewire.Func` / `rewire.Real` / `rewire.Restore`. Extend to also match the `*ByName` variants. Straightforward.

## Testing plan

- **Add an unexported function to `example/bar/`**:
  ```go
  func greet(name string) string { return "hello " + name }
  func Welcome(name string) string { return greet(name) + "!" }  // public entry
  ```
- **Scanner unit test**: ensure `rewire.FuncByName(t, "github.com/.../bar.greet", mock)` gets extracted correctly, even though `greet` is lowercase.
- **End-to-end test** in `example/foo/unexported_test.go`: call `rewire.FuncByName(t, "github.com/GiGurra/rewire/example/bar.greet", ...)`, invoke `bar.Welcome`, verify the mock fires.
- **Type-mismatch test**: pass a replacement with the wrong signature, verify the `t.Fatal` diagnostic names both types.
- **Wrong name test**: pass a name that doesn't exist, verify the "function not found" diagnostic.

## Edge cases

- **Method targets via string**: `FuncByName(t, "github.com/.../bar.(*Greeter).greet", mock)` should work identically. Parsing needs to recognize the method syntax.
- **Interaction with `isGenericFunc`**: the detection currently parses the target package's production source. It still works for unexported targets since it just matches by name and receiver type.
- **Compile-time detection of missing targets**: if the generated registration references a non-existent `&bar.Mock_notarealfunc`, the registration file fails to compile and the user sees a clear error pointing at the target. That's arguably better than a runtime "not found" at test time.

## Effort estimate

- Scanner: ~50 lines — new AST case for string-literal args, string-to-canonical-name parser.
- Codegen: minimal — existing registration emission already works once the scanner feeds it the right target name.
- `pkg/rewire`: ~150 lines — `FuncByName`, `RealByName`, `RestoreByName`, plus a reflect-based type checker.
- Tests: ~200 lines.
- Docs: new page `docs/unexported.md`, link from `docs/function-mocking.md`.

## Exit criteria

- `rewire.FuncByName(t, "<importPath>.greet", replacement)` successfully mocks an unexported function and the test can verify behavior via a public entry point that calls the unexported function.
- Clear runtime error for wrong target name, wrong replacement type, or non-function replacement.
- Method syntax in the string (`(*Type).Method`, `Type.Method`) works identically.
- Documented with a tradeoff note that the typed API should be preferred when available.
