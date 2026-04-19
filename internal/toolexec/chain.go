package toolexec

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
)

// Chain captures the optional `--and-then` chain of successor
// preprocessor invocations. A non-empty NextCmd means rewire was
// invoked as one link in a chain like:
//
//	rewire [rewire-args...] --and-then pre2 [pre2-args...] --and-then pre3 ... /abs/go/tool args...
//
// When the chain fires, rewire still does its normal rewriting, but
// instead of exec'ing the real Go tool directly it exec's NextCmd
// with the tool path (and rewritten tool args) appended. Each link
// in the chain peels off one --and-then and forwards the remainder,
// so the last link sees a classic toolexec invocation and needs no
// --and-then awareness at all.
type Chain struct {
	// NextCmd is the argv that should replace the bare tool invocation.
	// Empty when no --and-then was present.
	NextCmd []string
}

// parseChain splits the rewire toolexec args (i.e. os.Args[1:]) into
// its components:
//
//	rewireArgs — flags intended for rewire itself (reserved for future use; empty for now)
//	chain      — the successor command, if --and-then was used
//	toolPath   — the absolute path of the Go tool rewire wraps
//	toolArgs   — the tool's own arguments
//
// Without --and-then the parse is the classic toolexec shape:
// args[0] is the tool, args[1:] its arguments. With --and-then, rewire
// consumes everything up to the first --and-then as its own args,
// then scans forward to locate the Go tool (by absolute path + known
// tool base name). Everything between the --and-then and the Go tool
// is handed through as the successor argv.
//
// ok is false when args look like a broken chain invocation — e.g.
// a --and-then with no identifiable Go tool after it.
func parseChain(args []string) (rewireArgs []string, chain Chain, toolPath string, toolArgs []string, ok bool) {
	split := -1
	for i, a := range args {
		if a == "--and-then" {
			split = i
			break
		}
	}
	if split == -1 {
		if len(args) == 0 {
			return nil, Chain{}, "", nil, false
		}
		return nil, Chain{}, args[0], args[1:], true
	}
	rewireArgs = args[:split]
	rest := args[split+1:]
	toolIdx := findGoToolIndex(rest)
	if toolIdx < 0 {
		return rewireArgs, Chain{}, "", nil, false
	}
	chain = Chain{NextCmd: rest[:toolIdx]}
	toolPath = rest[toolIdx]
	toolArgs = rest[toolIdx+1:]
	return rewireArgs, chain, toolPath, toolArgs, true
}

// goToolBases is the set of basename identifiers Go's toolchain uses
// for the tools invoked via -toolexec. Locating the Go tool in a
// chain argv requires both:
//
//  1. the basename (minus any .exe suffix) to be in this set, and
//  2. the directory to equal $GOROOT/pkg/tool/$GOOS_$GOARCH/
//
// The directory check is the critical one: without it, a
// preprocessor flag value like `--output /tmp/compile` would be
// misclassified as the `compile` tool, silently truncating the
// chain and feeding garbage to the compiler.
//
// Source: $GOROOT/src/cmd/go/internal/work/exec.go and the contents
// of $GOROOT/pkg/tool/$GOOS_$GOARCH/.
var goToolBases = map[string]bool{
	"compile":   true,
	"link":      true,
	"asm":       true,
	"cgo":       true,
	"vet":       true,
	"nm":        true,
	"objdump":   true,
	"pack":      true,
	"buildid":   true,
	"addr2line": true,
	"covdata":   true,
	"test2json": true,
	"trace":     true,
	"fix":       true,
}

// goToolDir returns $GOROOT/pkg/tool/$GOOS_$GOARCH, the canonical
// directory Go's build system places its tool binaries under. The
// answer is invariant for the life of the process, so we cache it.
//
// We avoid runtime.GOROOT() because it was deprecated in Go 1.24:
// the path baked into the running binary is meaningless if the
// binary is run on a machine other than the one it was built on.
// Shelling out to `go env GOROOT` matches the rest of this package
// (see goroot() in intrinsics.go) and reports the GOROOT of
// whichever go toolchain is on $PATH — which is the toolchain
// actually running this build.
var goToolDir = sync.OnceValue(func() string {
	return filepath.Join(goroot(), "pkg", "tool", runtime.GOOS+"_"+runtime.GOARCH)
})

func findGoToolIndex(args []string) int {
	dir := goToolDir()
	for i, a := range args {
		if !filepath.IsAbs(a) {
			continue
		}
		if filepath.Dir(a) != dir {
			continue
		}
		base := strings.TrimSuffix(filepath.Base(a), ".exe")
		if goToolBases[base] {
			return i
		}
	}
	return -1
}

// HasAndThen reports whether argv (excluding argv[0]) contains a
// --and-then marker, i.e. whether rewire has been invoked as a
// chain link. cmd/rewire/main.go uses this to decide between CLI
// mode and toolexec mode when argv[1] is not an absolute path.
func HasAndThen(args []string) bool {
	for _, a := range args {
		if a == "--and-then" {
			return true
		}
	}
	return false
}

// execToolChained is the chain-aware analogue of execTool: when the
// chain has a successor, it exec's that successor with the tool and
// args appended; otherwise it falls through to the plain exec path.
func execToolChained(c Chain, tool string, args []string) int {
	if len(c.NextCmd) == 0 {
		return execTool(tool, args)
	}
	childArgs := append(slices.Clone(c.NextCmd[1:]), tool)
	childArgs = append(childArgs, args...)
	cmd := exec.Command(c.NextCmd[0], childArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "rewire: exec %s: %v\n", c.NextCmd[0], err)
		return 1
	}
	return 0
}

// execToolReplaceChained is the chain-aware analogue of
// execToolReplace: it tries to replace the current process with the
// successor (or the tool itself, if the chain is empty) via
// syscall.Exec, falling back to fork+wait on failure.
func execToolReplaceChained(c Chain, tool string, args []string) int {
	if len(c.NextCmd) == 0 {
		return execToolReplace(tool, args)
	}
	prog, err := exec.LookPath(c.NextCmd[0])
	if err != nil {
		// Fall back to fork+wait so the build still progresses.
		return execToolChained(c, tool, args)
	}
	argv := append(slices.Clone(c.NextCmd), tool)
	argv = append(argv, args...)
	if err := syscall.Exec(prog, argv, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "rewire: syscall.Exec failed for %s, falling back to fork+wait: %v\n", c.NextCmd[0], err)
		return execToolChained(c, tool, args)
	}
	// Unreachable on success.
	return 0
}
