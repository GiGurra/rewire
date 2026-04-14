// Package main is the rewire binary entrypoint. It has two modes:
//
//  1. toolexec mode — when invoked by `go test -toolexec=rewire`,
//     the first argument is an absolute path to a Go tool (typically
//     `compile`), and the rest are the tool's original arguments.
//     Rewire dispatches to toolexec.Run which handles rewriting,
//     interface mock synthesis, and passing through to the real
//     compiler.
//
//  2. CLI mode — the only human-facing subcommand is `rewrite`,
//     a debug helper that prints (or writes back) what the rewriter
//     would do to a single file. There is no user-facing entrypoint
//     for interface mock generation; that happens purely through
//     the toolexec pipeline.
//
// The CLI is intentionally minimal — plain stdlib `flag` package,
// no cobra, no boa. This keeps rewire's binary small and its startup
// fast, which matters because toolexec mode runs the rewire binary
// once per compile invocation and Go's build system invokes it a
// lot during `go test ./...`.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/GiGurra/rewire/internal/rewriter"
	"github.com/GiGurra/rewire/internal/toolexec"
)

const mainUsage = `rewire — compile-time Go mocking via -toolexec.

Usage as toolexec (primary mode):
  go test -toolexec=rewire ./...

Or set GOFLAGS for seamless IDE integration:
  export GOFLAGS="-toolexec=rewire"
  go test ./...          # rewire is active transparently

Interface mocks are generated transparently at compile time when a
test references rewire.NewMock[I] — no separate generate step
needed.

Subcommands:
  rewrite   Rewrite a Go source file to make functions mockable
            (debug helper — normal use is via -toolexec).

Run "rewire <subcommand> -h" for subcommand flags.
`

const rewriteUsage = `rewire rewrite — rewrite a single Go file so named functions become mockable.

Usage:
  rewire rewrite -f <file> -func <name> [-func <name>...] [-w]

This is a debug helper. Normal use is via -toolexec, which rewrites
the same functions automatically at compile time and doesn't touch
the source on disk.

For each named function, the rewriter produces:
  - A Mock_<Name> variable (nil by default, so production behavior
    is unchanged)
  - A wrapper that delegates to the mock if set, otherwise calls
    the original
  - The original function renamed to _real_<Name>

Flags:
`

func main() {
	// toolexec mode: when invoked by the Go toolchain with
	// `-toolexec=rewire`, argv[1] is an absolute path to a Go
	// tool. Dispatch there immediately, before any CLI flag
	// parsing — this is by far the hot path and we want it to
	// start as fast as possible.
	if len(os.Args) >= 2 && filepath.IsAbs(os.Args[1]) {
		os.Exit(toolexec.Run(os.Args[1:]))
	}

	// Otherwise we're in CLI mode. We don't use the top-level
	// `flag` package directly for subcommand dispatch because
	// Go's flag package doesn't have a notion of subcommands; we
	// parse argv[1] manually and delegate to per-subcommand
	// FlagSets.
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, mainUsage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "rewrite":
		runRewrite(os.Args[2:])
	case "-h", "--help", "help":
		_, _ = fmt.Fprint(os.Stdout, mainUsage)
	default:
		fmt.Fprintf(os.Stderr, "rewire: unknown subcommand %q\n\n", os.Args[1])
		fmt.Fprint(os.Stderr, mainUsage)
		os.Exit(2)
	}
}

// stringSliceFlag is the minimal flag.Value implementation needed
// for "-func NAME" repeat-flag support. Each `-func X` call appends
// X to the underlying slice.
type stringSliceFlag []string

func (s *stringSliceFlag) String() string {
	return strings.Join(*s, ",")
}

func (s *stringSliceFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func runRewrite(args []string) {
	fs := flag.NewFlagSet("rewrite", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, rewriteUsage)
		fs.PrintDefaults()
	}
	var (
		file     string
		funcs    stringSliceFlag
		writeOut bool
	)
	fs.StringVar(&file, "f", "", "Go source file to rewrite (required)")
	fs.Var(&funcs, "func", "function name to make mockable (repeatable)")
	fs.BoolVar(&writeOut, "w", false, "write result back to the file instead of stdout")
	_ = fs.Parse(args)

	if file == "" || len(funcs) == 0 {
		fs.Usage()
		os.Exit(2)
	}

	src, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading %s: %v\n", file, err)
		os.Exit(1)
	}

	for _, fn := range funcs {
		src, err = rewriter.RewriteSource(src, fn)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error rewriting function %q: %v\n", fn, err)
			os.Exit(1)
		}
	}

	if writeOut {
		if err := os.WriteFile(file, src, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "error writing %s: %v\n", file, err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "rewired %d function(s) in %s\n", len(funcs), file)
	} else {
		_, _ = os.Stdout.Write(src)
	}
}
