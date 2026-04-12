package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/GiGurra/boa/pkg/boa"
	"github.com/GiGurra/rewire/internal/mockgen"
	"github.com/GiGurra/rewire/internal/rewriter"
	"github.com/GiGurra/rewire/internal/toolexec"
	"github.com/spf13/cobra"
)

type RewriteParams struct {
	File  string   `descr:"Go source file to rewrite" short:"f"`
	Func  []string `descr:"function name(s) to make mockable"`
	Write bool     `descr:"write result back to the file instead of stdout" short:"w" optional:"true"`
}

type MockParams struct {
	File      string `descr:"Go source file containing the interface" short:"f"`
	Interface string `descr:"interface name to generate a mock for" short:"i"`
	Pkg       string `descr:"output package name (default: inferred from source file)" short:"p" optional:"true"`
}

func main() {
	// When invoked as -toolexec, the first arg is an absolute path to a Go tool
	// (e.g. /path/to/go/tool/compile). Detect this and dispatch to toolexec mode.
	if len(os.Args) >= 2 && filepath.IsAbs(os.Args[1]) {
		os.Exit(toolexec.Run(os.Args[1:]))
	}

	boa.CmdT[boa.NoParams]{
		Use:   "rewire",
		Short: "Rewire Go function calls for test-time mocking",
		Long: `Rewire makes Go functions mockable at test time without modifying production source.

Usage as toolexec (primary mode):
  go test -toolexec=rewire ./...

Or set GOFLAGS for seamless IDE integration:
  export GOFLAGS="-toolexec=rewire"
  go test ./...  # rewire is active transparently`,
		SubCmds: boa.SubCmds(
			boa.CmdT[RewriteParams]{
				Use:   "rewrite",
				Short: "Rewrite a Go source file to make functions mockable (for debugging)",
				Long: `Rewrite transforms function declarations so they can be swapped at test time.

For each specified function, it generates:
  - A Mock_<Name> variable (nil by default, so production behavior is unchanged)
  - A wrapper that delegates to the mock if set, otherwise calls the original
  - The original function renamed to _real_<Name>`,
				RunFunc: runRewrite,
			},
			boa.CmdT[MockParams]{
				Use:   "mock",
				Short: "Generate a mock struct implementing an interface",
				Long: `Generate a mock implementation of a Go interface.

The mock struct has function fields for each method. Set them in tests
to control behavior; unset methods return zero values.

Example:
  rewire mock -f bar.go -i Greeter
  rewire mock -f bar.go -i Greeter -p bar_test > mock_greeter_test.go`,
				RunFunc: runMock,
			},
		),
	}.Run()
}

func runRewrite(params *RewriteParams, _ *cobra.Command, _ []string) {
	src, err := os.ReadFile(params.File)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading %s: %v\n", params.File, err)
		os.Exit(1)
	}

	for _, fn := range params.Func {
		src, err = rewriter.RewriteSource(src, fn)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error rewriting function %q: %v\n", fn, err)
			os.Exit(1)
		}
	}

	if params.Write {
		if err := os.WriteFile(params.File, src, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "error writing %s: %v\n", params.File, err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "rewired %d function(s) in %s\n", len(params.Func), params.File)
	} else {
		os.Stdout.Write(src)
	}
}

func runMock(params *MockParams, _ *cobra.Command, _ []string) {
	src, err := os.ReadFile(params.File)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error reading %s: %v\n", params.File, err)
		os.Exit(1)
	}

	outputPkg := params.Pkg
	if outputPkg == "" {
		outputPkg = mockgen.InferPackageName(src)
	}

	out, err := mockgen.GenerateMock(src, params.Interface, outputPkg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error generating mock: %v\n", err)
		os.Exit(1)
	}

	os.Stdout.Write(out)
}
