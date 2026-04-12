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
