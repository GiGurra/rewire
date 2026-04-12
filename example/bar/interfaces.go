package bar

// Greeter greets people.
type GreeterIface interface {
	Greet(name string) string
}

// Store is a simple key-value store.
type Store interface {
	Get(key string) (string, error)
	Set(key string, value string) error
	Delete(key string) error
}

// Logger logs messages.
type Logger interface {
	Log(msg string)
	Logf(format string, args ...any)
}
