# Interface Mocks

Rewire supports two styles of interface mocking. The newer approach — `rewire.NewMock[T]` — synthesizes the backing struct at compile time via toolexec and is likely to become rewire's default interface-mocking API. Rewire's own older `rewire mock` CLI (usually invoked via `go:generate`) is still fully supported but is a candidate for deprecation inside rewire once the toolexec path reaches feature parity.

| Style | Trigger | Committed files | IDE visibility | Status in rewire |
|---|---|---|---|---|
| `rewire.NewMock[T]` (toolexec) | Just reference it in a test | None | Hidden (see below) | **Recommended for new code** (Phase 1 — simple interfaces) |
| `rewire mock` CLI | `go generate` or manual invocation | `mock_*_test.go` | Full — committed files are real Go source | Supported, candidate for deprecation inside rewire |

Both styles coexist today. The toolexec style requires no generation step and leaves no mock files in your repo; the CLI style is useful when you want to inspect or review the generated code.

## Toolexec mocks: `rewire.NewMock[T]`

No `go:generate`. No committed files. Just reference the interface in a test and the toolexec wrapper emits a backing struct at compile time.

```go
package foo_test

import (
    "testing"

    "github.com/example/bar"
    "github.com/GiGurra/rewire/pkg/rewire"
)

func TestService_GreetingFlow(t *testing.T) {
    greeter := rewire.NewMock[bar.GreeterIface](t)

    rewire.InstanceMethod(t, greeter, bar.GreeterIface.Greet, func(g bar.GreeterIface, name string) string {
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

rewire.InstanceMethod(t, g1, bar.GreeterIface.Greet, func(g bar.GreeterIface, name string) string { return "g1: " + name })
rewire.InstanceMethod(t, g2, bar.GreeterIface.Greet, func(g bar.GreeterIface, name string) string { return "g2: " + name })

g1.Greet("Alice") // "g1: Alice"
g2.Greet("Bob")   // "g2: Bob"
```

Unstubbed methods return zero values:

```go
greeter := rewire.NewMock[bar.GreeterIface](t)
greeter.Greet("Alice") // ""  — no stub, returns the zero value
```

Clear every stub on a mock with `rewire.Restore`:

```go
rewire.Restore(t, greeter) // drops every per-instance stub on greeter
```

Individual stubs can be cleared with `rewire.RestoreInstanceMethod(t, greeter, bar.GreeterIface.Greet)`.

### How it works

When the toolexec wrapper compiles your test package, it scans `_test.go` files for `rewire.NewMock[X]` references. For each interface it finds, it locates the interface's source, parses the method set, and synthesizes a backing struct into the test package's compile args:

```go
// Synthesized at compile time, never written to disk:
type _rewire_mock_bar_GreeterIface struct{ _ [1]byte }

var Mock__rewire_mock_bar_GreeterIface_Greet_ByInstance sync.Map

func (m *_rewire_mock_bar_GreeterIface) Greet(name string) (_r0 string) {
    // per-instance dispatch — same mechanism that backs
    // rewire.InstanceMethod for rewritten concrete methods.
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
- **Variadic parameters, multi-return, unnamed parameters**
- **Multiple mocks of the same interface** — scoped independently via per-instance dispatch
- **Multiple instantiations of the same generic interface** — `Container[int]` and `Container[string]` produce distinct backing structs and don't collide

```go
// All of these work:
g  := rewire.NewMock[bar.GreeterIface](t)              // non-generic
ci := rewire.NewMock[bar.Container[int]](t)            // generic, single type arg
cs := rewire.NewMock[bar.Container[string]](t)         // distinct instantiation
c  := rewire.NewMock[bar.Cache[string, int]](t)        // multi type args
n  := rewire.NewMock[bar.Container[bar.Container[int]]](t)  // nested generic
e  := rewire.NewMock[bar.Container[time.Duration]](t)  // external package type arg
```

Not yet supported (rejected with clear errors):

- Embedded interfaces (`io.ReadCloser` embeds `io.Reader` + `io.Closer`)
- Types from the interface's own declaring package (e.g. a method returning `*Greeter` where `Greeter` is defined in the same package as `GreeterIface`)

### Trade-offs vs the CLI / `go:generate` style

**IDE visibility.** The generated struct only exists during the compile. Gopls and other tooling can't see it. We deliberately designed the API so users never need to name the struct — you pass `rewire.NewMock[bar.GreeterIface]` for creation and `bar.GreeterIface.Greet` for stubbing, both of which the IDE understands. In practice the generated type is invisible and the cost disappears.

**Build speed.** At compile time, rewire reads the interface's source and generates a file. This adds a small per-test-package overhead proportional to the number of mocked interfaces. Negligible in practice, but not free.

**Reviewability.** You can't eyeball a committed `mock_*.go` file anymore, since there isn't one. If you want to inspect what the toolexec generated, use the CLI style instead — its output is a real Go source file.

## CLI mocks: `rewire mock` + `go:generate`

!!! note "Deprecation candidate inside rewire"
    This style was the original interface-mocking API in rewire. It's still fully supported and will remain so for the foreseeable future, but the toolexec style above covers the common case with less ceremony and the long-term plan is to make `rewire.NewMock[T]` rewire's canonical interface-mock API. The toolexec path now handles generic interfaces (added in Phase 2a); embedded interfaces and types from the interface's declaring package are the remaining gaps. Once those land too, rewire's CLI mock generator may be marked deprecated. This is purely about rewire's own internal API surface; nothing is being said about the `go:generate` ecosystem in general.

For interfaces you pass in (dependency injection), rewire generates lightweight mock structs via the `rewire mock` CLI. This is standard code generation — no toolexec required.

## Generating a mock

Given an interface:

```go
// bar/interfaces.go
package bar

type Store interface {
    Get(key string) (string, error)
    Set(key string, value string) error
    Delete(key string) error
}
```

Generate a mock:

```bash
rewire mock -f bar/interfaces.go -i Store -p foo -o mock_store_test.go
```

This produces:

```go
package foo

type MockStore struct {
    GetFunc    func(key string) (string, error)
    SetFunc    func(key string, value string) error
    DeleteFunc func(key string) error
}

func (m *MockStore) Get(key string) (_r0 string, _r1 error) {
    if m.GetFunc != nil {
        return m.GetFunc(key)
    }
    return
}

func (m *MockStore) Set(key string, value string) (_r0 error) {
    if m.SetFunc != nil {
        return m.SetFunc(key, value)
    }
    return
}

func (m *MockStore) Delete(key string) (_r0 error) {
    if m.DeleteFunc != nil {
        return m.DeleteFunc(key)
    }
    return
}
```

Each method has a corresponding function field. Unset fields return zero values.

## Using mocks in tests

```go
func TestGetOrDefault_Found(t *testing.T) {
    mock := &MockStore{
        GetFunc: func(key string) (string, error) {
            if key == "name" {
                return "Alice", nil
            }
            return "", errors.New("not found")
        },
    }

    got := GetOrDefault(mock, "name", "default")
    // got == "Alice"
}

func TestGetOrDefault_NotFound(t *testing.T) {
    mock := &MockStore{
        GetFunc: func(key string) (string, error) {
            return "", errors.New("not found")
        },
    }

    got := GetOrDefault(mock, "missing", "fallback")
    // got == "fallback"
}
```

## Unset methods return zero values

You only need to set the methods your test cares about:

```go
mock := &MockStore{} // all methods return zero values
resp, err := mock.Get("key")
// resp == "", err == nil
```

## Call tracking

Since replacements are closures, you can track calls:

```go
var setCalls []string
mock := &MockStore{
    SetFunc: func(key, value string) error {
        setCalls = append(setCalls, key+"="+value)
        return nil
    },
}

// ... run code under test ...

if len(setCalls) != 2 {
    t.Errorf("expected 2 Set calls, got %d", len(setCalls))
}
```

## External package types

The generator handles imported types in parameters and return values:

```go
type HTTPClient interface {
    Do(ctx context.Context, req *http.Request) (*http.Response, error)
    Upload(ctx context.Context, url string, body io.Reader) (int64, error)
}
```

```bash
rewire mock -f bar/interfaces.go -i HTTPClient -p foo -o mock_httpclient_test.go
```

The generated mock includes the correct imports (`context`, `net/http`, `io`) automatically.

## go:generate workflow

Add directives to your test files so mocks regenerate automatically:

```go
//go:generate rewire mock -f ../bar/interfaces.go -i Store -p foo -o mock_store_test.go
//go:generate rewire mock -f ../bar/interfaces.go -i Logger -p foo -o mock_logger_test.go
//go:generate rewire mock -f ../bar/interfaces.go -i HTTPClient -p foo -o mock_httpclient_test.go
```

Then:

```bash
go generate ./...   # regenerate mocks after interface changes
go test ./...       # run tests
```

## Command reference

```
rewire mock -f <source-file> -i <interface-name> [-p <package>] [-o <output-file>]
```

| Flag | Description | Default |
|------|-------------|---------|
| `-f` | Go source file containing the interface | (required) |
| `-i` | Interface name to generate a mock for | (required) |
| `-p` | Package name for generated code | inferred from source |
| `-o` | Output file path | stdout |

## What's supported

- Multiple methods with any signature
- Imported types (`context.Context`, `*http.Request`, `io.Reader`, etc.)
- Variadic parameters (`args ...any`)
- Unnamed parameters (auto-named `p0`, `p1`, etc.)
- Multiple return values
- Only directly-referenced imports are included in generated code

## Current limitations

These limits apply to the **CLI generator** (`rewire mock`) only. The toolexec `rewire.NewMock[T]` path above already handles generic interfaces — if you need generic interface mocking, use that instead.

- Embedded interfaces are not resolved — only methods directly declared on the interface
- Generic interfaces are not supported
