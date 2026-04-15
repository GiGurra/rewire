# Interface Mocks

Rewire mocks Go interfaces via `rewire.NewMock[T]`. The toolexec wrapper synthesizes a concrete backing struct at compile time, triggered purely by referencing the interface in a test file. No `go:generate`, no committed mock files, no separate CLI invocation.

## Quick start

Just reference the interface in a test and the toolexec wrapper emits a backing struct at compile time.

```go
package foo_test

import (
    "testing"

    "github.com/example/bar"
    "github.com/GiGurra/rewire/pkg/rewire"
)

func TestService_GreetingFlow(t *testing.T) {
    greeter := rewire.NewMock[bar.GreeterIface](t)

    rewire.InstanceFunc(t, greeter, bar.GreeterIface.Greet, func(g bar.GreeterIface, name string) string {
        return "mocked: " + name
    })

    svc := NewService(greeter)
    got := svc.HelloFlow("Alice")
    // ...
}
```

Stubs are per-instance, so two mocks of the same interface are independent:

```go
g1 := rewire.NewMock[bar.GreeterIface](t)
g2 := rewire.NewMock[bar.GreeterIface](t)

rewire.InstanceFunc(t, g1, bar.GreeterIface.Greet, func(g bar.GreeterIface, name string) string { return "g1: " + name })
rewire.InstanceFunc(t, g2, bar.GreeterIface.Greet, func(g bar.GreeterIface, name string) string { return "g2: " + name })

g1.Greet("Alice") // "g1: Alice"
g2.Greet("Bob")   // "g2: Bob"
```

Unstubbed methods return zero values:

```go
greeter := rewire.NewMock[bar.GreeterIface](t)
greeter.Greet("Alice") // ""  — no stub, returns the zero value
```

Clear every stub on a mock with `rewire.RestoreInstance`:

```go
rewire.RestoreInstance(t, greeter) // drops every per-instance stub on greeter
```

Individual stubs can be cleared with `rewire.RestoreInstanceFunc(t, greeter, bar.GreeterIface.Greet)`.

### How it works

When the toolexec wrapper compiles your test package, it scans `_test.go` files for `rewire.NewMock[X]` references. For each interface it finds, it locates the interface's source, parses the method set, and synthesizes a backing struct into the test package's compile args:

```go
// Synthesized at compile time, never written to disk:
type _rewire_mock_bar_GreeterIface struct{ _ [1]byte }

var Mock__rewire_mock_bar_GreeterIface_Greet_ByInstance sync.Map

func (m *_rewire_mock_bar_GreeterIface) Greet(name string) (_r0 string) {
    // per-instance dispatch — same mechanism that backs
    // rewire.InstanceFunc for rewritten concrete methods.
    ...
}

func init() {
    rewire.RegisterMockFactory("github.com/example/bar.GreeterIface", func() any {
        return &_rewire_mock_bar_GreeterIface{}
    })
    rewire.RegisterByInstance(
        "github.com/example/bar.GreeterIface.Greet",
        &Mock__rewire_mock_bar_GreeterIface_Greet_ByInstance,
    )
}
```

`rewire.NewMock[bar.GreeterIface](t)` looks up the factory by the interface's fully-qualified name and returns a fresh instance typed as `bar.GreeterIface`. The generated method's body consults the per-instance dispatch table — the exact same `ByInstance` mechanism that backs [per-instance method mocks](method-mocking.md#per-instance-method-mocks).

### Current scope

Supported today:

- **Non-generic interfaces** — any number of methods, any signature
- **Generic interfaces** — single and multi-type-parameter, with arbitrary type arguments:
    - Builtins (`int`, `string`, `bool`, etc.)
    - Slices, maps, channels, function types
    - Pointers (`*time.Time`)
    - External package types (`context.Context`, `*http.Request`)
    - Nested generic instantiations (`Container[Container[int]]`)
    - Types from the test package itself (`Container[*User]`)
- **Methods using imported types** — `context.Context`, `io.Reader`, etc.
- **Methods referencing same-package types as bare identifiers** — an interface in `bar/` can return `*Greeter` (without qualifying it as `*bar.Greeter`), and the generator automatically qualifies it when synthesizing the backing struct into the test package.
- **Dot imports in the interface's declaring file** — `import . "pkg"` brings the dot-imported package's exported names into the file's top-level scope; the generator detects the dot import, lists the dot-imported package's exported types, and qualifies bare identifiers with the dot-imported alias (so `Reader` resolves to `io.Reader`, not `declaringpkg.Reader`). Bare-ident embeds pointing at dot-imported interfaces are handled the same way — they're treated as cross-package embeds.
- **Module-aware package resolution** — `replace` directives in `go.mod`, workspace files (`go.work`), and vendor directories are all honored when locating an interface's source. Package lookup goes through `go list` so rewire's resolution is in lock-step with the surrounding Go build system.
- **Variadic parameters, multi-return, unnamed parameters**
- **Multiple mocks of the same interface** — scoped independently via per-instance dispatch
- **Multiple instantiations of the same generic interface** — `Container[int]` and `Container[string]` produce distinct backing structs and don't collide
- **Embedded interfaces** — same-file, same-package, and cross-package embeds all work. The full promoted method set is materialized on the mock, including methods from stdlib embeds like `io.Reader`. Generic embeds where the outer interface's type parameter flows into the embed are supported (e.g. `Outer[U]` embedding `Base[U]` instantiated as `Outer[int]` gives a `Base[int]` method set).

```go
// All of these work:
g  := rewire.NewMock[bar.GreeterIface](t)              // non-generic
ci := rewire.NewMock[bar.Container[int]](t)            // generic, single type arg
cs := rewire.NewMock[bar.Container[string]](t)         // distinct instantiation
c  := rewire.NewMock[bar.Cache[string, int]](t)        // multi type args
n  := rewire.NewMock[bar.Container[bar.Container[int]]](t)  // nested generic
e  := rewire.NewMock[bar.Container[time.Duration]](t)  // external package type arg
rc := rewire.NewMock[bar.ReadCloser](t)                // embeds io.Reader + same-pkg Named
lr := rewire.NewMock[bar.ListRepo[int]](t)             // generic embed: ListRepo[U] embeds Base[U]
gf := rewire.NewMock[bar.GreeterFactory](t)            // bare same-pkg *Greeter auto-qualified
```

Stubbing a promoted method uses the OUTER interface as the receiver in the method expression — that's what Go's runtime reports for method expressions on types with embeds:

```go
rc := rewire.NewMock[bar.ReadCloser](t)
// Read is promoted from io.Reader but stubbed via bar.ReadCloser.Read:
rewire.InstanceFunc(t, rc, bar.ReadCloser.Read, func(r bar.ReadCloser, p []byte) (int, error) {
    return copy(p, "hi"), nil
})
```

### Trade-offs

**IDE visibility.** The synthesized backing struct only exists during compilation. Gopls and other tooling can't see it. We deliberately designed the API so users never need to name the struct — you pass `rewire.NewMock[bar.GreeterIface]` for creation and `bar.GreeterIface.Greet` for stubbing, both of which the IDE understands. In practice the generated type is invisible and the cost disappears.

**Build speed.** At compile time, rewire reads the interface's source and synthesizes a backing-struct file per instantiation. This adds a small per-test-package overhead proportional to the number of mocked interfaces. Negligible in practice, but not free.

**Reviewability.** There's no committed `mock_*.go` file to eyeball, by design. If you ever need to see what the toolexec emitted, the temporary directory passed to the compiler is logged on errors and the synthesized file lives there until the compile finishes.
