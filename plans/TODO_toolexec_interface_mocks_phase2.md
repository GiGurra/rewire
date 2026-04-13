# TODO — Toolexec interface mocks, Phase 2

**Status:** not started
**Depends on:** Phase 1 (shipped in `db952be`).
**Layer:** `internal/mockgen/rewiregen.go` + `internal/toolexec/toolexec.go` (interface resolution), minor scanner adjustments.

## What Phase 1 does not handle

Phase 1 of `rewire.NewMock[T]` works for the simple end of the interface spectrum. Three specific gaps need Phase 2 work before it can retire the CLI-based `rewire mock` workflow:

1. **Embedded interfaces** (`io.ReadCloser` = `io.Reader` + `io.Closer`). Phase 1 rejects these with a clear error.
2. **Types from the interface's own declaring package** — e.g. an interface in `bar/` that exposes `func New() *Greeter` where `Greeter` is also in `bar/`. Phase 1 would emit `New() *Greeter` unqualified, which fails to compile in the test package because `Greeter` isn't in scope there.
3. **Cross-package types that `go/build` can't find without help.** Today we use `go/build.Default.Import` which respects `GOPATH` and module paths but doesn't do full resolution across replace directives, workspace files, or vendored copies. This is mostly fine in practice but could bite someone with unusual module setups.

Each is independently tractable and the plan below treats them as separate milestones inside Phase 2.

## Milestone A — Qualify types from the interface's own declaring package

### The problem

An interface file like `example/bar/iface.go`:

```go
package bar

type Greeter struct{ Prefix string }

type GreeterIface interface {
    New(name string) *Greeter   // ← "*Greeter" is an unqualified Ident in the AST
    Reset()
}
```

Phase 1's emitter walks the AST and prints method signatures verbatim using `go/printer`. The resulting generated file (emitted into the test package) contains `New(name string) *Greeter`, which the test package can't compile because `Greeter` is undefined there.

### The fix

When emitting method signatures, rewrite every bare `*ast.Ident` that names a type in the declaring package into a package-qualified `*ast.SelectorExpr` (`*Greeter` → `*bar.Greeter`). The generated file already imports the declaring package (we added it in Phase 1 precisely so the interface type itself is in scope), so the qualification just reuses that alias.

**Identifying "a type in the declaring package"** is the tricky bit. The AST alone can't tell us whether `Greeter` is a type or a value, local or imported. Options:

- **Option A — Heuristic over the declaring file.** Collect every top-level type name in the interface's source file (via `go/ast`), treat any bare Ident matching one of them as a type reference. Cheap, runs off the file we already parsed. Risk: misses types declared in OTHER files of the same package. If `Greeter` lives in `bar/greeter.go` but `GreeterIface` lives in `bar/iface.go`, the heuristic under-qualifies.
- **Option B — Heuristic over the whole declaring package.** Parse every `.go` file in the package directory (skipping test files), collect all top-level type names, use that set. Covers the multi-file case. Slightly slower — but still trivially fast at test-compile time, and we already walk the package dir to find the interface.
- **Option C — `go/types` for real type resolution.** Accurate in all cases. Heavyweight — requires setting up a `types.Config`, an `importer.Default()`, and running Check. Slowest path.

**Recommendation:** Option B. It handles the realistic multi-file-per-package case, is essentially as cheap as Option A, and avoids `go/types`' setup ceremony. If Option B ever gets a false positive (e.g. a method parameter named after a local type), we can fall back to Option C narrowly.

Keep the builtin types (`bool`, `int`, `string`, `error`, `any`, `comparable`, `uintptr`, `rune`, `byte`, the sized numeric types) in a local blocklist so we never accidentally qualify them.

### Tests

- Unit test in `internal/mockgen/rewiregen_test.go` covering an interface whose methods reference a struct declared in the same package.
- End-to-end test in `example/foo/` with a new interface in `example/bar/iface.go` that exposes same-package types.

## Milestone B — Embedded interfaces

### The problem

```go
package bar

type Reader interface {
    Read(p []byte) (int, error)
}

type Closer interface {
    Close() error
}

type ReadCloser interface {
    Reader
    Closer
    Flush() error
}
```

Phase 1 rejects `ReadCloser` entirely because its AST has three entries in `Methods.List` but only one of them (`Flush`) has a `Names` slice — the other two are embedded interfaces represented as bare type references. The emitter currently errors the moment it sees a list entry with no names.

### The fix

Two sub-problems: (a) discover the transitive method set, (b) generate a struct that implements all of them.

**Discovering the method set.** For each embedded interface reference in `Methods.List` (an entry where `Names` is empty and `Type` is a type expression), resolve the target:

- **Local embed** (`Reader` in the same package as `ReadCloser`): look it up in the declaring package's own types (same approach as Milestone A).
- **Cross-package embed** (`io.Reader` inside `bar.ReadCloser`): resolve `io.Reader` the same way Phase 1 resolves top-level interfaces — via `go/build` → find the source file → parse it. Recursive: if `io.Reader` itself embeds another interface, recurse until you have a flat method set.

Detect cycles defensively (interface A embeds B embeds A — illegal in Go but worth erroring cleanly on if the input is malformed).

Normalize: if the same method appears via two embeds, it must have identical signatures (Go already enforces this at compile time, so we can just use the first occurrence; conflicts will surface as compile errors on the generated file, which is a reasonable fallback).

**Generating the struct.** After flattening, the method list is just a longer version of the Phase 1 list. The existing template handles arbitrary method counts. No change needed beyond plumbing the flattened list into the template.

**Imports.** The generated file needs imports for every package referenced in the transitive method signatures, which may include packages from the embedded interfaces' source files that weren't imported by the outer interface's file. Extend `collectPkgRefs` to walk the flattened set.

### Tests

- Unit test for `io.ReadCloser` (cross-package embed).
- Unit test for a purely-local embed chain (`Bigger` embeds `Small` embeds nothing) in the same package.
- Unit test for a mixed case (local + cross-package embeds in the same interface).
- End-to-end test in `example/foo/` using a real composed interface — probably easier to add a new interface to `example/bar/interfaces.go` than to use `io.ReadCloser`, since `io.Reader.Read` requires allocating byte slices.

## Milestone C — Module-aware package resolution

### The problem

`go/build.Default.Import(importPath, "", build.FindOnly)` works for standard module setups but doesn't consult the module's `go.mod` replace directives, workspace files, or vendor directories. Users with unusual setups (monorepos with replace-based cross-links, `go work` workspaces, vendored dependencies) may hit "package not found" even though the compiler itself can find the code.

### The fix

Switch the resolver to `go list -json -find <importPath>` with `-mod=` matching the test build's mode. `go list` uses the same resolution the compiler does, so anything the compiler can find, `go list` can too.

Caveat: `go list` is slower than `go/build` (it forks a subprocess). For Phase 1's performance budget (one resolution per mocked interface per test compile) this is fine — typical test packages mock a handful of interfaces at most. For bulk resolution we could cache per build session via the existing flock-protected cache mechanism in `scan.go`.

Catch: the recursive `go list` call must strip `GOFLAGS=-toolexec=rewire` from its environment, otherwise it re-invokes rewire's own toolexec wrapper and recurses forever. We already do this for `ensureStdImportsInCfg`'s `go list -export` call — reuse the same pattern.

### Tests

- Unit test that runs the resolver against a package using a replace directive in a throwaway module (tricky to set up cleanly; might skip and rely on manual verification).
- End-to-end test that mocks an interface from a module referenced via replace — probably requires `example/foo/` to add a tiny replace-based sibling module.

If Milestone C feels disproportionate to its value, consider deferring it and documenting the limitation instead. The user base most affected (monorepo + replace directives) can always fall back to the CLI-generated mock style in the meantime.

## Ordering and effort

| Milestone | Est. lines (incl. tests) | Unlocks |
|---|---|---|
| A — same-package type qualification | ~200 | Most real-world interfaces in user code |
| B — embedded interfaces | ~300 | `io.ReadCloser`-style composition; common in stdlib and middleware layers |
| C — module-aware resolution | ~150 | Edge-case module setups (replace, workspaces, vendor) |

Do A first — it unblocks the largest class of real interfaces and is the simplest of the three. B next — embedded interfaces are common enough in stdlib-mimicking code that users will hit this fast. C last, and only if a user actually reports a resolution failure.

## Non-goals for Phase 2

- Generic interfaces. Those belong in Phase 3 and use the per-instantiation pattern we already have for generic method rewriting.
- Performance optimization of interface resolution. Phase 1's per-compile-call approach is fine; no caching yet.
- Parity with `rewire mock` CLI's exact output shape. The toolexec path uses a different internal struct shape (function-field → per-instance dispatch). As long as both pass their tests and produce working mocks, drift is OK.

## Exit criteria for Phase 2

- `rewire.NewMock[T]` works for any interface in user code whose methods use:
  - Builtin types
  - Types from the interface's own declaring package (Milestone A)
  - Types from any imported package the interface's file already imports
- Embedded interfaces (local or cross-package) are resolved transitively and the generated struct implements the full method set (Milestone B)
- Module-aware resolution handles replace directives and workspaces (Milestone C, if tackled)
- All existing Phase 1 tests continue to pass
- Docs in `docs/interface-mocks.md` updated to move the listed Phase 2 items out of the "not yet supported" section
