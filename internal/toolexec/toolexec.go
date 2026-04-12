package toolexec

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/GiGurra/rewire/internal/rewriter"
)

// Run executes the toolexec wrapper. It is called when rewire is invoked as:
//
//	rewire /path/to/go/tool/compile <args...>
//
// For compile invocations targeting same-module packages, it rewrites source
// files to add mock variables, writes the rewritten files to a temp directory,
// and invokes the real compiler with the modified file paths.
func Run(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "rewire toolexec: missing tool argument")
		return 1
	}

	tool := args[0]
	toolArgs := args[1:]

	// Only intercept the compile tool
	if !isCompileTool(tool) {
		return execTool(tool, toolArgs)
	}

	modulePath := findModulePath()
	if modulePath == "" {
		// Can't determine module — pass through without rewriting
		return execTool(tool, toolArgs)
	}

	pkgPath := findFlag(toolArgs, "-p")
	if pkgPath == "" || !isInModule(pkgPath, modulePath) {
		return execTool(tool, toolArgs)
	}

	// Rewrite source files in the compile args
	rewrittenArgs, cleanup, err := rewriteCompileArgs(toolArgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rewire: rewrite failed for %s: %v\n", pkgPath, err)
		return 1
	}
	if cleanup != nil {
		defer cleanup()
	}

	return execTool(tool, rewrittenArgs)
}

func isCompileTool(tool string) bool {
	base := filepath.Base(tool)
	return base == "compile"
}

func isInModule(pkgPath, modulePath string) bool {
	return pkgPath == modulePath || strings.HasPrefix(pkgPath, modulePath+"/")
}

// findModulePath reads the module path from go.mod, searching upward from CWD.
func findModulePath() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err == nil {
			return parseModulePath(string(data))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func parseModulePath(gomod string) string {
	for _, line := range strings.Split(gomod, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module"))
		}
	}
	return ""
}

// findFlag returns the value of a flag like "-p foo" from the args.
func findFlag(args []string, flag string) string {
	for i, arg := range args {
		if arg == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// rewriteCompileArgs scans compile args for .go source files, rewrites them,
// and returns modified args pointing to the rewritten temp files.
func rewriteCompileArgs(args []string) ([]string, func(), error) {
	tmpDir, err := os.MkdirTemp("", "rewire-*")
	if err != nil {
		return nil, nil, fmt.Errorf("creating temp dir: %w", err)
	}
	cleanup := func() { os.RemoveAll(tmpDir) }

	newArgs := make([]string, len(args))
	copy(newArgs, args)

	for i, arg := range newArgs {
		if !strings.HasSuffix(arg, ".go") {
			continue
		}
		// Skip test files — they contain test code, not functions to mock
		if strings.HasSuffix(arg, "_test.go") {
			continue
		}

		src, err := os.ReadFile(arg)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("reading %s: %w", arg, err)
		}

		rewritten, err := rewriter.RewriteAllExported(src)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("rewriting %s: %w", arg, err)
		}

		// If nothing changed, keep original path
		if string(rewritten) == string(src) {
			continue
		}

		tmpFile := filepath.Join(tmpDir, filepath.Base(arg))
		if err := os.WriteFile(tmpFile, rewritten, 0644); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("writing temp file: %w", err)
		}
		newArgs[i] = tmpFile
	}

	return newArgs, cleanup, nil
}

func execTool(tool string, args []string) int {
	cmd := exec.Command(tool, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintf(os.Stderr, "rewire: exec %s: %v\n", tool, err)
		return 1
	}
	return 0
}
