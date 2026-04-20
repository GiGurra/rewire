package foo

import (
	"testing"

	"github.com/GiGurra/rewire/example/transit_iface"
	"github.com/GiGurra/rewire/pkg/rewire"
	// Intentionally does NOT import transit_types. The interface
	// transit_iface.Service references transit_types.Payload in its
	// method signature, but only the interface's declaring package
	// imports transit_types. Rewire's generated mock must therefore
	// add transit_types to the compile's -importcfg on its own —
	// Go's build system doesn't put it there because no source file
	// in this test package mentions it.
)

// Compiling this test is the whole assertion: if the generated mock
// file's new import of transit_types can't be resolved by the Go
// compiler (because it isn't in -importcfg), the test binary fails
// to build with "could not import ... (open : no such file or
// directory)". A passing compile means rewire patched -importcfg to
// include transit_types.
func TestNewMock_TransitiveImportFromMethodSignature(t *testing.T) {
	_ = rewire.NewMock[transit_iface.Service](t)
}
