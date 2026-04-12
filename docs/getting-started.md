# Getting Started

## Installation

You need two things: the rewire binary (for toolexec and mock generation) and the library (for `rewire.Func` in tests).

```bash
# Install the rewire binary
go install github.com/GiGurra/rewire/cmd/rewire@latest

# Add the test library to your module
go get github.com/GiGurra/rewire/pkg/rewire
```

## Function mocking (quick start)

### 1. Clean the build cache

Rewire needs to recompile packages through its toolexec wrapper. Clear the cache once so cached packages get recompiled:

```bash
go clean -cache
```

### 2. Write a test using `rewire.Func`

```go
package foo

import (
    "testing"
    "example/bar"
    "github.com/GiGurra/rewire/pkg/rewire"
)

func TestWelcome_WithMock(t *testing.T) {
    rewire.Func(t, bar.Greet, func(name string) string {
        return "Howdy, " + name
    })

    got := Welcome("Alice")
    if got != "Welcome! Howdy, Alice" {
        t.Errorf("got %q", got)
    }
}
```

### 3. Run with toolexec

```bash
GOFLAGS="-toolexec=rewire" go test ./...
```

That's it. `bar.Greet` is replaced for the duration of `TestWelcome_WithMock` and automatically restored after.

See [Setup](setup.md) for IDE configuration (GoLand, VS Code) and recommended cache strategies.

## Interface mock generation (quick start)

### 1. Generate a mock

Given an interface in `bar/interfaces.go`:

```go
type Store interface {
    Get(key string) (string, error)
    Set(key string, value string) error
}
```

Generate a mock:

```bash
rewire mock -f bar/interfaces.go -i Store -p foo -o mock_store_test.go
```

### 2. Use it in tests

```go
func TestGetOrDefault(t *testing.T) {
    mock := &MockStore{
        GetFunc: func(key string) (string, error) {
            return "value", nil
        },
    }
    got := GetOrDefault(mock, "key", "default")
    // got == "value"
}
```

No toolexec needed for interface mocks — they're plain Go structs.

### 3. Automate with go:generate

Add directives to your test file:

```go
//go:generate rewire mock -f ../bar/interfaces.go -i Store -p foo -o mock_store_test.go
```

Then regenerate anytime interfaces change:

```bash
go generate ./...
```

## Next steps

- [Function Mocking](function-mocking.md) — detailed guide with examples
- [Method Mocking](method-mocking.md) — mock struct methods
- [Interface Mocks](interface-mocks.md) — full mock generation guide
- [Setup](setup.md) — IDE and terminal configuration
