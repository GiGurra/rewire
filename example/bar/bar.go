package bar

import "fmt"

func Greet(name string) string {
	return fmt.Sprintf("Hello, %s!", name)
}

type Greeter struct {
	Prefix string
}

func (g *Greeter) Greet(name string) string {
	return g.Prefix + ", " + name + "!"
}

// TinyAdd and TinyDouble are deliberately tiny leaf functions that the Go
// inliner is aggressive about. They exist to verify that rewire's wrapper
// still fires even when the target function gets inlined into its callers.
func TinyAdd(a, b int) int { return a + b }

func TinyDouble(x int) int { return x * 2 }
