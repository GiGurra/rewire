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

func (g *Greeter) Farewell(name string) string {
	return "Bye " + name + " from " + g.Prefix
}

// TinyAdd and TinyDouble are deliberately tiny leaf functions that the Go
// inliner is aggressive about. They exist to verify that rewire's wrapper
// still fires even when the target function gets inlined into its callers.
func TinyAdd(a, b int) int { return a + b }

func TinyDouble(x int) int { return x * 2 }

// Map is a generic function used to verify rewire's per-instantiation
// mocking for generics. Each type-argument combination (e.g. Map[int,string]
// vs Map[float64,bool]) can be mocked independently.
func Map[T, U any](in []T, f func(T) U) []U {
	out := make([]U, len(in))
	for i, v := range in {
		out[i] = f(v)
	}
	return out
}

// Container is a generic type used to verify rewire's mocking of methods
// on generic types. Mocking (*Container[int]).Add must only replace the
// int instantiation — other instantiations still run the real body.
type Container[T any] struct {
	items []T
}

func (c *Container[T]) Add(v T) {
	c.items = append(c.items, v)
}

func (c *Container[T]) Get(i int) T {
	return c.items[i]
}

func (c *Container[T]) Len() int {
	return len(c.items)
}
