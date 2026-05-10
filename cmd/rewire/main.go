// Package main is the legacy rewire binary entrypoint.
// It exists so `go install github.com/GiGurra/rewire/cmd/rewire@latest`
// keeps working — new installs should prefer the shorter
// `go install github.com/GiGurra/rewire@latest` form, which builds
// the identical binary from the root main.go.
//
// All logic lives in internal/cli; this is just a shim.
package main

import "github.com/GiGurra/rewire/internal/cli"

func main() { cli.Run() }
