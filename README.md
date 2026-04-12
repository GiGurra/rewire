# rewire

Compile-time function mocking for Go. Replace any exported function during tests — no interfaces, no dependency injection, no unsafe runtime patches.

Production source stays **100% clean**. Rewire works by intercepting the Go compiler via `-toolexec`, rewriting functions on the fly to add mock variables that only exist at compile time.

Inspired by Erlang's [meck](https://github.com/eproxus/meck).

## Quick start

```bash
# Install
go install github.com/GiGurra/rewire/cmd/rewire@latest

# Set GOFLAGS (add to your shell profile for persistence)
export GOFLAGS="-toolexec=rewire"

# Clear build cache (one-time, after setting GOFLAGS)
go clean -cache

# Run tests — rewire is active transparently
go test ./...
```

## Usage

Given production code like this:

```go
// bar/bar.go — never modified
package bar

func Greet(name string) string {
    return fmt.Sprintf("Hello, %s!", name)
}
```

You can mock it in tests:

```go
// foo/foo_test.go
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
    // bar.Greet now returns "Howdy, Alice" — restored automatically after test
}

func TestWelcome_Real(t *testing.T) {
    got := Welcome("Bob")
    // bar.Greet uses the real implementation here
}
```

That's it. Pass the original function and its replacement. No mock variable names, no generated types, no interface wrappers.

## How it works

When `go test -toolexec=rewire` compiles a same-module package, rewire:

1. **Rewrites exported functions** — adds a `Mock_` variable and nil-check wrapper per function
2. **Generates a registration file** — for test compilations, maps function names to mock variable pointers in a runtime registry
3. **Passes rewritten source to the real compiler** — source on disk is never touched

At test time, `rewire.Func` uses `runtime.FuncForPC` to resolve the original function name, looks up the mock variable pointer in the registry, and swaps it via `reflect`. `t.Cleanup` restores the original after each test.

The rewrite transformation (only exists during compilation):

```go
var Mock_Greet func(name string) string

func Greet(name string) string {
    if f := Mock_Greet; f != nil {
        return f(name)
    }
    return _real_Greet(name)
}

func _real_Greet(name string) string {
    return fmt.Sprintf("Hello, %s!", name)
}
```

When `Mock_Greet` is nil (the default), the function behaves identically to the original — just one nil check, which the branch predictor handles at near-zero cost.

## IDE integration (IntelliJ / GoLand / VS Code)

Set `GOFLAGS` once and your IDE's click-to-run test works transparently:

```bash
export GOFLAGS="-toolexec=rewire"
```

Add this to your shell profile (`~/.bashrc`, `~/.zshrc`, `~/.config/fish/config.fish`) or set it in your IDE's project environment variables.

In **GoLand**: Run > Edit Configurations > Templates > Go Test > Environment variables > add `GOFLAGS=-toolexec=rewire`.

### Build cache note

Go's build cache keys on compilation inputs. When switching between toolexec and non-toolexec builds, you may need to run `go clean -cache` to force a recompile. This is typically a one-time step after initial setup.

If you forget, `rewire.Func` will fail with a clear error message showing your current `GOFLAGS` and step-by-step fix instructions.

## Test isolation

Each `go test` package compiles into a separate binary. This means:
- `foo`'s tests can mock `bar.Greet`
- `baz`'s tests can use the real `bar.Greet`
- No configuration needed — each test binary is independent

Within a test package, `rewire.Func` uses `t.Cleanup` to restore the original after each test.

## API

### `rewire.Func` (recommended)

```go
rewire.Func(t, bar.Greet, func(name string) string {
    return "mocked"
})
```

Takes the original function by reference and a replacement with the same signature. Requires `-toolexec=rewire` to be active. Produces a clear error with setup instructions if toolexec is not active.

### `rewire.Replace` (low-level)

```go
rewire.Replace(t, &bar.Mock_Greet, func(name string) string {
    return "mocked"
})
```

Directly swaps a mock variable by pointer. Useful if you need explicit control over mock variable names, but requires knowing the generated `Mock_` variable name.

## Limitations

- **Exported functions only** — unexported functions are not rewritten
- **No methods** — only package-level functions (method support is planned)
- **No generics** — generic functions are skipped
- **No parallel mock safety** — parallel tests in the same package should not mock the same function with different replacements
- **Same module only** — only packages within the same Go module are rewritten (third-party dependencies are not touched)
- **Build cache** — switching between toolexec/non-toolexec requires `go clean -cache`

## Project structure

```
cmd/rewire/          CLI entry point (toolexec mode + manual rewrite subcommand)
pkg/rewire/          Test helper library (Func and Replace)
internal/rewriter/   AST-based source rewriter
internal/toolexec/   Toolexec wrapper with registration file generation
example/             End-to-end example
docs/                Design docs and decision log
```

## License

MIT
