package toolexec

import (
	"path/filepath"
	"reflect"
	"testing"
)

// toolPath returns an absolute path that points into the real
// $GOROOT/pkg/tool/$GOOS_$GOARCH/ directory, which is what
// findGoToolIndex accepts as a Go tool location. Hardcoded stub
// paths like "/goroot/..." no longer suffice after the GOTOOLDIR
// tightening in chain.go.
func toolPath(name string) string {
	return filepath.Join(goToolDir(), name)
}

func TestParseChain_Classic(t *testing.T) {
	// No --and-then: args[0] is the tool, args[1:] are tool args.
	_, chain, tool, toolArgs, ok := parseChain([]string{toolPath("compile"), "-p", "foo", "a.go"})
	if !ok {
		t.Fatalf("parse failed")
	}
	if len(chain.NextCmd) != 0 {
		t.Errorf("expected no chain, got %v", chain.NextCmd)
	}
	if tool != toolPath("compile") {
		t.Errorf("tool=%q", tool)
	}
	if !reflect.DeepEqual(toolArgs, []string{"-p", "foo", "a.go"}) {
		t.Errorf("toolArgs=%v", toolArgs)
	}
}

func TestParseChain_OneHop(t *testing.T) {
	args := []string{
		"--and-then", "proven",
		toolPath("compile"), "-p", "foo", "a.go",
	}
	_, chain, tool, toolArgs, ok := parseChain(args)
	if !ok {
		t.Fatalf("parse failed")
	}
	if !reflect.DeepEqual(chain.NextCmd, []string{"proven"}) {
		t.Errorf("chain=%v", chain.NextCmd)
	}
	if tool != toolPath("compile") {
		t.Errorf("tool=%q", tool)
	}
	if !reflect.DeepEqual(toolArgs, []string{"-p", "foo", "a.go"}) {
		t.Errorf("toolArgs=%v", toolArgs)
	}
}

func TestParseChain_MultiHop(t *testing.T) {
	args := []string{
		"--rewire-flag",
		"--and-then", "proven", "--proven-flag",
		"--and-then", "third", "--third-flag",
		toolPath("compile"), "-p", "foo", "a.go",
	}
	rewireArgs, chain, tool, toolArgs, ok := parseChain(args)
	if !ok {
		t.Fatalf("parse failed")
	}
	if !reflect.DeepEqual(rewireArgs, []string{"--rewire-flag"}) {
		t.Errorf("rewireArgs=%v", rewireArgs)
	}
	wantChain := []string{"proven", "--proven-flag", "--and-then", "third", "--third-flag"}
	if !reflect.DeepEqual(chain.NextCmd, wantChain) {
		t.Errorf("chain=%v want=%v", chain.NextCmd, wantChain)
	}
	if tool != toolPath("compile") {
		t.Errorf("tool=%q", tool)
	}
	if !reflect.DeepEqual(toolArgs, []string{"-p", "foo", "a.go"}) {
		t.Errorf("toolArgs=%v", toolArgs)
	}
}

func TestParseChain_AsmTool(t *testing.T) {
	args := []string{
		"--and-then", "proven",
		toolPath("asm"), "input.s",
	}
	_, chain, tool, toolArgs, ok := parseChain(args)
	if !ok {
		t.Fatalf("parse failed")
	}
	if !reflect.DeepEqual(chain.NextCmd, []string{"proven"}) {
		t.Errorf("chain=%v", chain.NextCmd)
	}
	if tool != toolPath("asm") {
		t.Errorf("tool=%q", tool)
	}
	if !reflect.DeepEqual(toolArgs, []string{"input.s"}) {
		t.Errorf("toolArgs=%v", toolArgs)
	}
}

func TestParseChain_AbsolutePrePath(t *testing.T) {
	// Preprocessor binary passed as absolute path (not just name).
	// The go-tool locator requires the canonical GOTOOLDIR prefix,
	// so /usr/local/bin/proven is skipped and the compile tool
	// (under the real $GOROOT/pkg/tool/...) is found.
	args := []string{
		"--and-then", "/usr/local/bin/proven", "--flag",
		toolPath("compile"), "-p", "foo",
	}
	_, chain, tool, _, ok := parseChain(args)
	if !ok {
		t.Fatalf("parse failed")
	}
	if !reflect.DeepEqual(chain.NextCmd, []string{"/usr/local/bin/proven", "--flag"}) {
		t.Errorf("chain=%v", chain.NextCmd)
	}
	if tool != toolPath("compile") {
		t.Errorf("tool=%q", tool)
	}
}

func TestParseChain_FlagValueNamedLikeTool(t *testing.T) {
	// Regression: a preprocessor flag value like `--output /tmp/compile`
	// must NOT be misclassified as the go-compile tool. The real
	// tool is under $GOROOT/pkg/tool/..., and the directory check in
	// findGoToolIndex must skip the bogus path.
	args := []string{
		"--and-then", "instrumenter", "--output", "/tmp/compile",
		toolPath("compile"), "-p", "foo",
	}
	_, chain, tool, _, ok := parseChain(args)
	if !ok {
		t.Fatalf("parse failed")
	}
	wantChain := []string{"instrumenter", "--output", "/tmp/compile"}
	if !reflect.DeepEqual(chain.NextCmd, wantChain) {
		t.Errorf("chain=%v want=%v", chain.NextCmd, wantChain)
	}
	if tool != toolPath("compile") {
		t.Errorf("tool=%q — decoy path won; real tool misidentified", tool)
	}
}

func TestParseChain_ExeSuffix(t *testing.T) {
	// Verify .exe basename stripping. True Windows paths can't run
	// on a Linux host (filepath.IsAbs rejects `C:\...`), but the
	// trim code runs on any OS, so exercising it with a POSIX path
	// under the current GOTOOLDIR is sufficient.
	args := []string{
		"--and-then", "proven",
		toolPath("compile.exe"), "-p", "foo",
	}
	_, _, tool, _, ok := parseChain(args)
	if !ok {
		t.Fatalf("parse failed")
	}
	if tool != toolPath("compile.exe") {
		t.Errorf("tool=%q", tool)
	}
}

func TestParseChain_NoToolAfterAndThen(t *testing.T) {
	// --and-then present but no recognizable Go tool after it — parse
	// fails so the caller can emit a diagnostic rather than silently
	// doing nothing.
	args := []string{"--and-then", "proven", "--flag"}
	_, _, _, _, ok := parseChain(args)
	if ok {
		t.Fatalf("expected parse failure for chain with no tool")
	}
}

func TestHasAndThen(t *testing.T) {
	if !HasAndThen([]string{"--and-then", "proven"}) {
		t.Errorf("HasAndThen: expected true for [--and-then, proven]")
	}
	if !HasAndThen([]string{"--flag", "--and-then", "proven"}) {
		t.Errorf("HasAndThen: expected true when marker is not first")
	}
	if HasAndThen([]string{"/abs/tool", "-p", "foo"}) {
		t.Errorf("HasAndThen: expected false for classic args")
	}
	if HasAndThen(nil) {
		t.Errorf("HasAndThen: expected false for nil")
	}
}
