package foo

import (
	"math"

	"github.com/GiGurra/rewire/example/bar"
)

func Welcome(name string) string {
	return "Welcome! " + bar.Greet(name)
}

func SquareRoot(x float64) float64 {
	return math.Pow(x, 0.5)
}

func GreetWith(g *bar.Greeter, name string) string {
	return g.Greet(name)
}

// QuadrupleViaTinyDouble calls bar.TinyDouble twice. Both bar.TinyDouble
// and this function are small enough that Go's inliner is highly likely
// to inline the call sites — which makes this a good end-to-end check
// that rewire's wrapper still takes effect under inlining.
func QuadrupleViaTinyDouble(x int) int {
	return bar.TinyDouble(bar.TinyDouble(x))
}

func SumViaTinyAdd(a, b, c int) int {
	return bar.TinyAdd(bar.TinyAdd(a, b), c)
}
