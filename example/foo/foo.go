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
