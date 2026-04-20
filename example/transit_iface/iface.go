// Package transit_iface declares an interface whose method signature
// references a type from a SEPARATE package (example/transit_types).
// Tests that mock this interface typically won't import transit_types
// directly — it's reached only through the interface's method
// parameter. That's the shape that reproduces the missing-importcfg
// bug in toolexec-generated mocks.
package transit_iface

import (
	"context"

	"github.com/GiGurra/rewire/example/transit_types"
)

// Service is an interface whose Handle method references
// transit_types.Payload — the key property: only this package's source
// imports transit_types, not the consumer test package.
type Service interface {
	Handle(ctx context.Context, p transit_types.Payload) (string, error)
}
