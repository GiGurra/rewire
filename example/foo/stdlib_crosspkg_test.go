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
