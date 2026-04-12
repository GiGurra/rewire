# Interface Mock Generation

For interfaces you pass in (dependency injection), rewire generates lightweight mock structs. This is standard code generation — no toolexec required.

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

- Embedded interfaces are not resolved — only methods directly declared on the interface
- Generic interfaces are not supported
