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

## Method mocks are global

Method mocks apply to **all instances** of the type, not a specific object. When you mock `(*Greeter).Greet`, every `*Greeter` in the test uses the replacement:

```go
rewire.Func(t, (*bar.Greeter).Greet, func(g *bar.Greeter, name string) string {
    return "mocked"
})

g1 := &bar.Greeter{Prefix: "Hi"}
g2 := &bar.Greeter{Prefix: "Hey"}

g1.Greet("Alice") // "mocked"
g2.Greet("Bob")   // also "mocked"
```

This is consistent with how function mocking works — the mock variable is package-level. For per-instance behavior, use [interface mocks](interface-mocks.md) instead.

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
