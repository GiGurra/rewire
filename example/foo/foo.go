package foo

import "github.com/GiGurra/rewire/example/bar"

func Welcome(name string) string {
	return "Welcome! " + bar.Greet(name)
}
