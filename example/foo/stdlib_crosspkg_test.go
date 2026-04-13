package foo

import (
	"os"
	"path/filepath"
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
