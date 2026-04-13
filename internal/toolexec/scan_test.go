package toolexec

import (
	"os"
	"testing"
)

func TestScanFileForMockCalls_SimpleFunction(t *testing.T) {
	path := writeTempFile(t, `package foo

import (
	"testing"
	"example/bar"
	"github.com/GiGurra/rewire/pkg/rewire"
)

func TestMock(t *testing.T) {
	rewire.Func(t, bar.Greet, func(name string) string { return "" })
}
`)
	targets, _, _ := scanFileForMockCalls(path)
	assertTarget(t, targets, "example/bar", "Greet")
}

func TestScanFileForMockCalls_MultipleTargets(t *testing.T) {
	path := writeTempFile(t, `package foo

import (
	"math"
	"testing"
	"example/bar"
	"github.com/GiGurra/rewire/pkg/rewire"
)

func TestA(t *testing.T) {
	rewire.Func(t, bar.Greet, func(name string) string { return "" })
}

func TestB(t *testing.T) {
	rewire.Func(t, math.Pow, func(x, y float64) float64 { return 0 })
}
`)
	targets, _, _ := scanFileForMockCalls(path)
	assertTarget(t, targets, "example/bar", "Greet")
	assertTarget(t, targets, "math", "Pow")
}

func TestScanFileForMockCalls_AliasedRewireImport(t *testing.T) {
	path := writeTempFile(t, `package foo

import (
	"testing"
	"example/bar"
	rw "github.com/GiGurra/rewire/pkg/rewire"
)

func TestMock(t *testing.T) {
	rw.Func(t, bar.Greet, func(name string) string { return "" })
}
`)
	targets, _, _ := scanFileForMockCalls(path)
	assertTarget(t, targets, "example/bar", "Greet")
}

func TestScanFileForMockCalls_AliasedTargetImport(t *testing.T) {
	path := writeTempFile(t, `package foo

import (
	"testing"
	mybar "example/bar"
	"github.com/GiGurra/rewire/pkg/rewire"
)

func TestMock(t *testing.T) {
	rewire.Func(t, mybar.Greet, func(name string) string { return "" })
}
`)
	targets, _, _ := scanFileForMockCalls(path)
	assertTarget(t, targets, "example/bar", "Greet")
}

func TestScanFileForMockCalls_NoRewireImport(t *testing.T) {
	path := writeTempFile(t, `package foo

import "testing"

func TestPlain(t *testing.T) {}
`)
	targets, _, _ := scanFileForMockCalls(path)
	if len(targets) != 0 {
		t.Errorf("expected no targets, got %v", targets)
	}
}

func TestScanFileForMockCalls_PointerReceiverMethod(t *testing.T) {
	path := writeTempFile(t, `package foo

import (
	"testing"
	"example/bar"
	"github.com/GiGurra/rewire/pkg/rewire"
)

func TestMock(t *testing.T) {
	rewire.Func(t, (*bar.Server).Handle, func(s *bar.Server, r string) string { return "" })
}
`)
	targets, _, _ := scanFileForMockCalls(path)
	assertTarget(t, targets, "example/bar", "(*Server).Handle")
}

func TestScanFileForMockCalls_ValueReceiverMethod(t *testing.T) {
	path := writeTempFile(t, `package foo

import (
	"testing"
	"example/bar"
	"github.com/GiGurra/rewire/pkg/rewire"
)

func TestMock(t *testing.T) {
	rewire.Func(t, bar.Point.String, func(p bar.Point) string { return "" })
}
`)
	targets, _, _ := scanFileForMockCalls(path)
	assertTarget(t, targets, "example/bar", "Point.String")
}

func TestScanFileForMockCalls_MixedFunctionsAndMethods(t *testing.T) {
	path := writeTempFile(t, `package foo

import (
	"math"
	"testing"
	"example/bar"
	"github.com/GiGurra/rewire/pkg/rewire"
)

func TestA(t *testing.T) {
	rewire.Func(t, bar.Greet, func(name string) string { return "" })
}

func TestB(t *testing.T) {
	rewire.Func(t, (*bar.Server).Handle, func(s *bar.Server, r string) string { return "" })
}

func TestC(t *testing.T) {
	rewire.Func(t, math.Pow, func(x, y float64) float64 { return 0 })
}
`)
	targets, _, _ := scanFileForMockCalls(path)
	assertTarget(t, targets, "example/bar", "Greet")
	assertTarget(t, targets, "example/bar", "(*Server).Handle")
	assertTarget(t, targets, "math", "Pow")
}

func TestScanFileForMockCalls_DuplicateTargets(t *testing.T) {
	path := writeTempFile(t, `package foo

import (
	"testing"
	"example/bar"
	"github.com/GiGurra/rewire/pkg/rewire"
)

func TestA(t *testing.T) {
	rewire.Func(t, bar.Greet, func(name string) string { return "a" })
}

func TestB(t *testing.T) {
	rewire.Func(t, bar.Greet, func(name string) string { return "b" })
}
`)
	targets, _, _ := scanFileForMockCalls(path)
	assertTarget(t, targets, "example/bar", "Greet")
	// Should have Greet listed (dedup happens in scanAllTestFiles, not scanFileForMockCalls)
	count := 0
	for _, name := range targets["example/bar"] {
		if name == "Greet" {
			count++
		}
	}
	if count != 2 {
		t.Errorf("expected 2 raw entries for Greet before dedup, got %d", count)
	}
}

func TestMockVarName(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"Greet", "Mock_Greet"},
		{"(*Server).Handle", "Mock_Server_Handle"},
		{"Point.String", "Mock_Point_String"},
		{"(*DB).Query", "Mock_DB_Query"},
	}
	for _, tt := range tests {
		got := mockVarName(tt.input)
		if got != tt.want {
			t.Errorf("mockVarName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- helpers ---

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := dir + "/test_file_test.go"
	if err := writeFile(path, content); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}

func assertTarget(t *testing.T, targets mockTargets, importPath, funcName string) {
	t.Helper()
	funcs, ok := targets[importPath]
	if !ok {
		t.Errorf("expected targets for %q, got none. All targets: %v", importPath, targets)
		return
	}
	for _, f := range funcs {
		if f == funcName {
			return
		}
	}
	t.Errorf("expected %q in targets for %q, got %v", funcName, importPath, funcs)
}
