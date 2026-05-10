// Package main is the rewire binary entrypoint, placed at the
// module root so `go install github.com/GiGurra/rewire@latest`
// produces the `rewire` binary directly. All logic lives in
// internal/cli; this is just a shim.
package main

import "github.com/GiGurra/rewire/internal/cli"

func main() { cli.Run() }
