package foo

import (
	"testing"

	"github.com/GiGurra/rewire/example/aliased_dir"
	"github.com/GiGurra/rewire/pkg/rewire"
)

// The aliased_dir directory declares `package aliasedpkg`, so the
// default last-path-segment alias ("aliased_dir") is wrong: not only
// does it not match the Go identifier the test file uses, it contains
// an underscore that can't be a package selector on its own. This
// regression test guards the compile path — if the mock generator
// reverts to using the directory basename for the emitted qualifier,
// the generated backing struct won't compile.
func TestNewMock_PackageNameDiffersFromDirName(t *testing.T) {
	g := rewire.NewMock[aliasedpkg.Greeter](t)

	rewire.InstanceFunc(t, g, aliasedpkg.Greeter.Greet, func(_ aliasedpkg.Greeter, name string) string {
		return "hi, " + name
	})

	if got := g.Greet("Alice"); got != "hi, Alice" {
		t.Errorf("got %q, want %q", got, "hi, Alice")
	}
}
