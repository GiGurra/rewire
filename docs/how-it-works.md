# How It Works

Rewire uses Go's `-toolexec` flag to intercept the compiler during `go test`. It rewrites targeted functions in-memory — source files on disk are never modified.

## The pipeline

1. **Pre-scan** — On the first compiler invocation, rewire walks your module's `_test.go` files and parses them with `go/ast`. It finds all `rewire.Func` calls and builds a target list (e.g., `bar.Greet`, `math.Pow`, `(*Server).Handle`).

2. **Targeted rewrite** — When the compiler processes a package containing targeted functions, rewire:
    - Reads the source files from the compiler's argument list
    - Rewrites only the specific functions in the target list
    - Writes rewritten source to temp files
    - Passes the temp file paths to the real compiler

3. **Registration** — When compiling a test package, rewire generates an `init()` function that registers mock variable pointers in a runtime registry. This connects the test binary to the mock variables.

4. **Runtime swap** — At test time, `rewire.Func(t, bar.Greet, replacement)`:
    - Calls `runtime.FuncForPC(reflect.ValueOf(bar.Greet).Pointer())` to get the function's fully-qualified name
    - Looks up the mock variable pointer in the registry
    - Sets the mock variable to the replacement via `reflect`
    - Registers a `t.Cleanup` to restore the original

## The rewrite transformation

Only targeted functions are rewritten. Everything else passes through untouched.

Given:

```go
func Greet(name string) string {
    return "Hello, " + name + "!"
}
```

Rewire produces (in-memory, during compilation only):

```go
var Mock_Greet func(name string) string

func Greet(name string) string {
    if _rewire_mock := Mock_Greet; _rewire_mock != nil {
        return _rewire_mock(name)
    }
    return _real_Greet(name)
}

func _real_Greet(name string) string {
    return "Hello, " + name + "!"
}
```

The wrapper checks `Mock_Greet` on every call. When nil (the default), the original implementation runs — the overhead is a single nil check.

## Method rewriting

Methods follow the same pattern but include the receiver:

```go
// Original
func (s *Server) Handle(req string) string {
    return "handled " + req
}

// Rewritten (in-memory)
var Mock_Server_Handle func(*Server, string) string

func (s *Server) Handle(req string) string {
    if _rewire_mock := Mock_Server_Handle; _rewire_mock != nil {
        return _rewire_mock(s, req)
    }
    return s._real_Server_Handle(req)
}

func (s *Server) _real_Server_Handle(req string) string {
    return "handled " + req
}
```

## Build cache

Go's build cache keys on compilation inputs including the toolexec binary. The recommended setup uses a separate cache for tests:

```bash
GOFLAGS="-toolexec=rewire" GOCACHE="$HOME/.cache/rewire-test" go test ./...
```

This keeps `go build` (production) and `go test` (with rewire) from sharing cached artifacts.

## Compiler intrinsics

Some functions (e.g., `math.Abs`, `math.Sqrt`) are replaced by CPU instructions at the **call site** by the Go compiler. Even though rewire can rewrite the function body, callers bypass it entirely.

Rewire detects these by parsing the compiler's own intrinsics registry (`$GOROOT/src/cmd/compile/internal/ssagen/intrinsics.go`). If you try to mock an intrinsic, the build fails with a clear error:

```
rewire: error: function math.Abs cannot be mocked.
  It is a compiler intrinsic — the Go compiler replaces calls to it
  with a CPU instruction, bypassing any mock wrapper.
```

## Scan caching

The test file scan happens once per build session (keyed on parent PID). A file lock ensures only one toolexec process scans; others wait for the cached result. This avoids redundant work when `go test` invokes many parallel compiler processes.
