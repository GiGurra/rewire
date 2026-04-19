package toolexec

import (
	"reflect"
	"testing"
)

func TestParseChain_Classic(t *testing.T) {
	// No --and-then: args[0] is the tool, args[1:] are tool args.
	_, chain, tool, toolArgs, ok := parseChain([]string{"/goroot/pkg/tool/linux_amd64/compile", "-p", "foo", "a.go"})
	if !ok {
		t.Fatalf("parse failed")
	}
	if len(chain.NextCmd) != 0 {
		t.Errorf("expected no chain, got %v", chain.NextCmd)
	}
	if tool != "/goroot/pkg/tool/linux_amd64/compile" {
		t.Errorf("tool=%q", tool)
	}
	if !reflect.DeepEqual(toolArgs, []string{"-p", "foo", "a.go"}) {
		t.Errorf("toolArgs=%v", toolArgs)
	}
}

func TestParseChain_OneHop(t *testing.T) {
	args := []string{
		"--and-then", "proven",
		"/goroot/pkg/tool/linux_amd64/compile", "-p", "foo", "a.go",
	}
	_, chain, tool, toolArgs, ok := parseChain(args)
	if !ok {
		t.Fatalf("parse failed")
	}
	if !reflect.DeepEqual(chain.NextCmd, []string{"proven"}) {
		t.Errorf("chain=%v", chain.NextCmd)
	}
	if tool != "/goroot/pkg/tool/linux_amd64/compile" {
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
		"/goroot/pkg/tool/linux_amd64/compile", "-p", "foo", "a.go",
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
	if tool != "/goroot/pkg/tool/linux_amd64/compile" {
		t.Errorf("tool=%q", tool)
	}
	if !reflect.DeepEqual(toolArgs, []string{"-p", "foo", "a.go"}) {
		t.Errorf("toolArgs=%v", toolArgs)
	}
}

func TestParseChain_AsmTool(t *testing.T) {
	args := []string{
		"--and-then", "proven",
		"/goroot/pkg/tool/linux_amd64/asm", "input.s",
	}
	_, chain, tool, toolArgs, ok := parseChain(args)
	if !ok {
		t.Fatalf("parse failed")
	}
	if !reflect.DeepEqual(chain.NextCmd, []string{"proven"}) {
		t.Errorf("chain=%v", chain.NextCmd)
	}
	if tool != "/goroot/pkg/tool/linux_amd64/asm" {
		t.Errorf("tool=%q", tool)
	}
	if !reflect.DeepEqual(toolArgs, []string{"input.s"}) {
		t.Errorf("toolArgs=%v", toolArgs)
	}
}

func TestParseChain_AbsolutePrePath(t *testing.T) {
	// Preprocessor binary passed as absolute path (not just name).
	// The go-tool locator prefers known tool bases, so /usr/local/bin/proven
	// is skipped and the real compile tool is found.
	args := []string{
		"--and-then", "/usr/local/bin/proven", "--flag",
		"/goroot/pkg/tool/linux_amd64/compile", "-p", "foo",
	}
	_, chain, tool, _, ok := parseChain(args)
	if !ok {
		t.Fatalf("parse failed")
	}
	if !reflect.DeepEqual(chain.NextCmd, []string{"/usr/local/bin/proven", "--flag"}) {
		t.Errorf("chain=%v", chain.NextCmd)
	}
	if tool != "/goroot/pkg/tool/linux_amd64/compile" {
		t.Errorf("tool=%q", tool)
	}
}

func TestParseChain_WindowsExe(t *testing.T) {
	args := []string{
		"--and-then", "proven",
		`C:\Go\pkg\tool\windows_amd64\compile.exe`, "-p", "foo",
	}
	// filepath.IsAbs on the Linux host doesn't recognize the Windows
	// path as absolute, so this test only validates the basename
	// trimming logic on a POSIX-style absolute path:
	_ = args
	posixArgs := []string{
		"--and-then", "proven",
		"/goroot/pkg/tool/linux_amd64/compile.exe", "-p", "foo",
	}
	_, _, tool, _, ok := parseChain(posixArgs)
	if !ok {
		t.Fatalf("parse failed")
	}
	if tool != "/goroot/pkg/tool/linux_amd64/compile.exe" {
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
