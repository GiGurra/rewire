# Method Mocking

Replace struct methods at test time using Go's method expression syntax. Like function mocking, no interfaces or code changes needed.

## Pointer receiver methods

```go
func TestGreetWith_MockedMethod(t *testing.T) {
    rewire.Func(t, (*bar.Greeter).Greet, func(g *bar.Greeter, name string) string {
        return "Mocked, " + name
    })

    g := &bar.Greeter{Prefix: "Hi"}
    got := GreetWith(g, "Alice")
    // got == "Mocked, Alice"
}
```

The syntax `(*bar.Greeter).Greet` is a Go [method expression](https://go.dev/ref/spec#Method_expressions). It has type `func(*bar.Greeter, string) string` — the receiver becomes the first parameter. Your replacement function must match this signature.

## Value receiver methods

```go
func TestPoint_String(t *testing.T) {
    rewire.Func(t, bar.Point.String, func(p bar.Point) string {
        return "mocked point"
    })
}
```

For value receivers, use `Type.Method` (no parentheses or star).

## Global by default

Method mocks set via `rewire.Func` apply to **all instances** of the type, not a specific object. When you mock `(*Greeter).Greet`, every `*Greeter` in the test uses the replacement:

```go
rewire.Func(t, (*bar.Greeter).Greet, func(g *bar.Greeter, name string) string {
    return "mocked"
})

g1 := &bar.Greeter{Prefix: "Hi"}
g2 := &bar.Greeter{Prefix: "Hey"}

g1.Greet("Alice") // "mocked"
g2.Greet("Bob")   // also "mocked"
```

This is consistent with how function mocking works — the mock variable is package-level. If you want per-instance scoping without changing your production code to take an interface, see [Per-instance method mocks](#per-instance-method-mocks) below.

## Per-instance method mocks

`rewire.InstanceMethod` scopes a method replacement to one specific receiver. Other instances of the same type keep running the real implementation (or the global mock, if one is set).

```go
func TestInstanceMethod_ScopedToOneInstance(t *testing.T) {
    g1 := &bar.Greeter{Prefix: "Hi"}
    g2 := &bar.Greeter{Prefix: "Hello"}

    rewire.InstanceMethod(t, g1, (*bar.Greeter).Greet, func(g *bar.Greeter, name string) string {
        return "g1-mock: " + name
    })

    g1.Greet("Alice") // "g1-mock: Alice"  — per-instance mock
    g2.Greet("Bob")   // "Hello, Bob!"      — real body
}
```

The replacement's signature is the same as a `rewire.Func` method replacement: the receiver is the first parameter, followed by the method's own parameters.

### Dispatch order

Inside a method wrapper, rewire checks three places in order:

1. **Per-instance mock** (set via `rewire.InstanceMethod`) — matched by the receiver pointer.
2. **Global mock** (set via `rewire.Func`) — applies to any instance with no per-instance mock.
3. **Real implementation** — unchanged production code.

A per-instance mock always overrides a global mock for the same target:

```go
func TestInstanceMethod_OverridesGlobal(t *testing.T) {
    g1 := &bar.Greeter{Prefix: "Hi"}
    g2 := &bar.Greeter{Prefix: "Hello"}

    rewire.Func(t, (*bar.Greeter).Greet, func(g *bar.Greeter, name string) string {
        return "global: " + name
    })
    rewire.InstanceMethod(t, g1, (*bar.Greeter).Greet, func(g *bar.Greeter, name string) string {
        return "g1-mock: " + name
    })

    g1.Greet("Alice") // "g1-mock: Alice" — per-instance wins
    g2.Greet("Bob")   // "global: Bob"    — global fallback
}
```

### Multiple methods on one instance

You can set per-instance mocks for several methods on the same receiver. They're independent entries:

```go
rewire.InstanceMethod(t, g, (*bar.Greeter).Greet,    mockGreet)
rewire.InstanceMethod(t, g, (*bar.Greeter).Farewell, mockFarewell)
```

### Mid-test restore

Each `InstanceMethod` call registers its own `t.Cleanup`, so per-instance mocks are automatically restored at test end. Two helpers let you clear mocks earlier:

```go
// Clear every per-instance mock bound to this instance, for all methods.
rewire.Restore(t, g)

// Clear one specific per-instance entry, leaving other methods untouched.
rewire.RestoreInstanceMethod(t, g, (*bar.Greeter).Greet)
```

`rewire.Restore(t, target)` is overloaded: if `target` is a function/method expression, it clears the global mock (original behavior); if `target` is an instance value, it walks every per-method table and clears entries scoped to that instance.

### Generic methods

Per-instance mocking works for methods on generic types too. Scope applies per instantiation and per receiver:

```go
func TestInstanceMethod_GenericMethod(t *testing.T) {
    c1 := &bar.Container[int]{}
    c2 := &bar.Container[int]{}

    rewire.InstanceMethod(t, c1, (*bar.Container[int]).Add, func(c *bar.Container[int], v int) {
        // swallow — c1 never actually appends
    })

    c1.Add(1)  // routed to per-instance mock
    c2.Add(2)  // real body — c2.Len() == 1
}
```

Interface equality on the per-instance key compares both dynamic type and pointer value, so `*Container[int]` and `*Container[string]` entries never collide even at the same address.

### Restrictions

- **Pointer receivers only.** Value-receiver methods are copied on every call and have no stable identity to key on. The test fails immediately with a clear error.
- **Free functions are not supported.** There's nothing to scope to — use `rewire.Func` instead.
- **Requires `-toolexec=rewire`.** The compiler wrapper has to be emitted with per-instance support, which happens automatically whenever any test in the module references the target via `InstanceMethod` or `RestoreInstanceMethod`.

## Method expressions vs. method values

Rewire requires you to pass a Go **method expression** (`(*Type).Method` or `Type.Method`), not a **method value** (`instance.Method` bound to a specific receiver):

```go
g := &bar.Greeter{Prefix: "Hi"}

// ❌ Method value — bound to g. Rejected with a diagnostic.
rewire.Func(t, g.Greet, replacement)

// ✅ Method expression — unbound, receiver is first parameter.
rewire.Func(t, (*bar.Greeter).Greet, replacement)
```

Two reasons for the restriction:

1. **Semantic clarity.** Method mocks are global — they apply to every instance of the type. Passing `g.Greet` suggests "mock only this instance", which isn't what actually happens. The method-expression form makes the global scope visible in the code.
2. **Runtime identity.** `runtime.FuncForPC` reports method values with a `-fm` suffix (e.g., `bar.(*Greeter).Greet-fm`) because Go wraps them in a thunk that binds the receiver. That wrapper is not the function rewire rewrote, so the mock variable lookup would fail anyway.

If you accidentally pass a method value, rewire detects it and fails the test with a targeted error pointing to the method-expression form you should use instead.

## Methods on generic types

Methods on generic types (`func (c *Container[T]) Add(v T)`) are supported, and each type-argument combination is mocked independently — same API as non-generic methods:

```go
// Production: example/bar/bar.go
type Container[T any] struct {
    items []T
}

func (c *Container[T]) Add(v T) {
    c.items = append(c.items, v)
}
```

```go
// Test
func TestContainer_MockInt(t *testing.T) {
    rewire.Func(t, (*bar.Container[int]).Add, func(c *bar.Container[int], v int) {
        // mock body — doesn't delegate, so items stays empty
    })

    ci := &bar.Container[int]{}
    ci.Add(1)
    ci.Add(2)
    if ci.Len() != 0 {
        t.Errorf("int mock should have swallowed adds, got Len=%d", ci.Len())
    }

    // A different instantiation is untouched
    cs := &bar.Container[string]{}
    cs.Add("hello")
    if cs.Len() != 1 {
        t.Errorf("string instantiation should run real, got Len=%d", cs.Len())
    }
}
```

`rewire.Real` and `rewire.Restore` also work for generic methods. `rewire.Real(t, (*bar.Container[int]).Add)` returns a `func(*bar.Container[int], int)` that invokes the real method — pass the receiver as the first argument to call it.

The only thing rewire can't rewrite is a method that declares its *own* type parameters (`func (c *C) Method[X any]()`) — Go 1.18+ forbids that anyway, so it's not a practical gap.

## Closure capture

Just like function mocks, method replacements are closures:

```go
func TestDB_QueryTracking(t *testing.T) {
    var queries []string

    rewire.Func(t, (*bar.DB).Query, func(db *bar.DB, sql string, args ...any) ([]string, error) {
        queries = append(queries, sql)
        return nil, nil
    })

    // ... code that calls db.Query ...

    if len(queries) != 2 {
        t.Errorf("expected 2 queries, got %d", len(queries))
    }
}
```

## Requirements

Method mocking requires the toolexec wrapper, same as function mocking:

```bash
GOFLAGS="-toolexec=rewire" go test ./...
```

See [Setup](setup.md) for IDE and terminal configuration.
