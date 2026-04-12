# rewire

Compile-time function mocking for Go. Replace any exported function during tests — no interfaces, no dependency injection, no unsafe runtime patches.

Production source stays **100% clean**. Rewire works by intercepting the Go compiler via `-toolexec`, rewriting functions on the fly to add mock variables that only exist at compile time.

## Quick start

```bash
# Install
go install github.com/GiGurra/rewire/cmd/rewire@latest

# Run tests with rewire active
go test -toolexec=rewire ./...
```

## How it works

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
    "github.com/GiGurra/rewire/pkg/rewire"
    "example/bar"
)

func TestWelcome_WithMock(t *testing.T) {
    rewire.Replace(t, &bar.Mock_Greet, func(name string) string {
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

Under the hood, when `go test -toolexec=rewire` compiles `bar`, rewire transparently rewrites `Greet` into:

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

This only happens during compilation. The source file on disk is never touched.

## IDE integration (IntelliJ / GoLand / VS Code)

Set `GOFLAGS` once and your IDE's click-to-run test works transparently:

```bash
export GOFLAGS="-toolexec=rewire"
```

Add this to your shell profile (`~/.bashrc`, `~/.zshrc`, `~/.config/fish/config.fish`) or set it in your IDE's project environment variables.

In **GoLand**: Run > Edit Configurations > Templates > Go Test > Environment variables > add `GOFLAGS=-toolexec=rewire`.

**Note:** `Mock_` variables are generated at compile time and don't exist in source, so your IDE's static analysis may show errors in test files that reference them. The code compiles and runs correctly — this is a known v1 limitation (see [future work](docs/design.md#future-work)).

## Test isolation

Each `go test` package compiles into a separate binary. This means:
- `foo`'s tests can mock `bar.Greet`
- `baz`'s tests can use the real `bar.Greet`
- No configuration needed — each test binary is independent

Within a test package, `rewire.Replace` uses `t.Cleanup` to restore the original after each test.

## Limitations

- **Exported functions only** — unexported functions are not rewritten
- **No methods** — only package-level functions (method support is planned)
- **No generics** — generic functions are skipped
- **No parallel mock safety** — parallel tests in the same package should not mock the same function with different replacements
- **IDE analysis** — `Mock_` variables are invisible to gopls/IDE static analysis (the code still compiles and runs)
- **Same module only** — only packages within the same Go module are rewritten (third-party dependencies are not touched)

## Project structure

```
cmd/rewire/          CLI entry point (toolexec mode + manual rewrite subcommand)
pkg/rewire/          Test helper library (Replace function)
internal/rewriter/   AST-based source rewriter
internal/toolexec/   Toolexec wrapper logic
example/             End-to-end example
docs/                Design docs and decision log
```

## License

MIT
