package foo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GiGurra/rewire/pkg/rewire"
)

// Demonstrates mocking a stdlib function (os.Getwd) that is called
// transitively from another stdlib function (filepath.Abs) — both live
// outside this module, and neither is touched by foo/bar source code.
func TestFilepathAbs_WithMockedOsGetwd(t *testing.T) {
	rewire.Func(t, os.Getwd, func() (string, error) {
		return "/mocked", nil
	})

	got, err := filepath.Abs("foo")
	if err != nil {
		t.Fatal(err)
	}
	if got != "/mocked/foo" {
		t.Errorf("got %q, want /mocked/foo", got)
	}
}

func TestRestore_EndsMockMidTest(t *testing.T) {
	rewire.Func(t, os.Getwd, func() (string, error) {
		return "/mocked", nil
	})

	mocked, _ := filepath.Abs("foo")
	if mocked != "/mocked/foo" {
		t.Fatalf("precondition failed: mock not active, got %q", mocked)
	}

	rewire.Restore(t, os.Getwd)

	real, err := filepath.Abs("foo")
	if err != nil {
		t.Fatal(err)
	}
	if real == "/mocked/foo" {
		t.Errorf("Restore did not take effect: still got %q", real)
	}
}

func TestRestore_IsIdempotent(t *testing.T) {
	rewire.Func(t, os.Getwd, func() (string, error) {
		return "/mocked", nil
	})

	// Calling Restore repeatedly must be safe; the automatic cleanup
	// installed by Func will still run at test end without issue.
	rewire.Restore(t, os.Getwd)
	rewire.Restore(t, os.Getwd)
	rewire.Restore(t, os.Getwd)

	got, err := filepath.Abs("foo")
	if err != nil {
		t.Fatal(err)
	}
	if got == "/mocked/foo" {
		t.Errorf("expected real path after Restore, got %q", got)
	}
}

func TestRestore_WithoutPriorFunc(t *testing.T) {
	// Restore is safe to call even when Func was never invoked — the mock
	// variable is simply set to its zero value (which it already was).
	rewire.Restore(t, os.Getwd)

	got, err := filepath.Abs("foo")
	if err != nil {
		t.Fatal(err)
	}
	if got == "/mocked/foo" {
		t.Errorf("expected real path, got %q", got)
	}
}

// Spy pattern: capture the real os.Getwd, then install a mock that
// delegates to it and appends a suffix. This verifies that rewire.Real
// returns a callable reference to the pre-rewrite implementation.
func TestReal_SpyDelegatesToRealImplementation(t *testing.T) {
	realGetwd := rewire.Real(t, os.Getwd)

	rewire.Func(t, os.Getwd, func() (string, error) {
		realPath, err := realGetwd()
		if err != nil {
			return "", err
		}
		return realPath + "/wrapped", nil
	})

	got, err := filepath.Abs("foo")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "/wrapped/foo") {
		t.Errorf("expected path ending in /wrapped/foo, got %q", got)
	}
	if strings.Contains(got, "/mocked") {
		t.Errorf("path should not contain /mocked, got %q", got)
	}
}

// The real implementation should still work even when Real is captured
// inside the mock closure (not just outside it).
func TestReal_CallableFromInsideMockClosure(t *testing.T) {
	rewire.Func(t, os.Getwd, func() (string, error) {
		real := rewire.Real(t, os.Getwd)
		path, err := real()
		if err != nil {
			return "", err
		}
		return path + "/inner", nil
	})

	got, err := filepath.Abs("foo")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "/inner/foo") {
		t.Errorf("expected path ending in /inner/foo, got %q", got)
	}
}

// rewire.Real alone should trigger rewriting, with no rewire.Func call
// needed. strings.ToUpper is only referenced by Real in this module.
func TestReal_TriggersRewritingWithoutFunc(t *testing.T) {
	realUpper := rewire.Real(t, strings.ToUpper)
	got := realUpper("hello")
	if got != "HELLO" {
		t.Errorf("real strings.ToUpper(%q) = %q, want %q", "hello", got, "HELLO")
	}
}

// Call counting: verify that the spy delegates N times to the real
// implementation, one per call through filepath.Abs.
func TestReal_SpyCountsRealCalls(t *testing.T) {
	realGetwd := rewire.Real(t, os.Getwd)

	realCalls := 0
	rewire.Func(t, os.Getwd, func() (string, error) {
		realCalls++
		return realGetwd()
	})

	for range 5 {
		if _, err := filepath.Abs("foo"); err != nil {
			t.Fatal(err)
		}
	}

	if realCalls != 5 {
		t.Errorf("expected 5 delegated calls to real os.Getwd, got %d", realCalls)
	}
}

func TestHostname_Mocked(t *testing.T) {
	rewire.Func(t, os.Hostname, func() (string, error) {
		return "mocked-host", nil
	})

	got, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if got != "mocked-host" {
		t.Errorf("expected mocked-host, got %s", got)
	}
}
