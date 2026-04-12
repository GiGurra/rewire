package foo

type MockGreeterIface struct {
	GreetFunc func(name string) string
}

func (m *MockGreeterIface) Greet(name string) (_r0 string) {
	if m.GreetFunc != nil {
		return m.GreetFunc(name)
	}
	return
}
