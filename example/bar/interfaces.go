package bar

import (
	"context"
	"io"
	"net/http"
)

// Greeter greets people.
type GreeterIface interface {
	Greet(name string) string
}

// HTTPClient abstracts HTTP operations with external package types.
type HTTPClient interface {
	Do(ctx context.Context, req *http.Request) (*http.Response, error)
	Upload(ctx context.Context, url string, body io.Reader) (int64, error)
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

// ContainerIface is a generic single-type-parameter interface used to
// exercise rewire.NewMock support for generic interfaces. Add and Get
// reference the type parameter T; Len does not, so it also verifies
// that methods unrelated to T are still generated correctly.
type ContainerIface[T any] interface {
	Add(v T)
	Get(i int) T
	Len() int
}

// CacheIface is a generic multi-type-parameter interface (K, V) used
// to exercise rewire.NewMock support for instantiations with multiple
// type arguments.
type CacheIface[K comparable, V any] interface {
	Set(k K, v V)
	Get(k K) (V, bool)
}

// Repository is the canonical generic data-access interface — the
// shape you'd expect to find in any real Go service. It exists in the
// example package to demonstrate rewire.NewMock with a "production
// flow" pattern: a service struct depends on Repository[T], the
// service is wired up in production with a real implementation, and
// in tests the dependency is satisfied by a generated mock.
//
// The type parameter shows up in multiple positions (return value,
// slice return, value parameter), and the methods take a
// context.Context — both are the kinds of things that exercise the
// type-parameter substitution and external-package import collection
// in the toolexec mock generator.
type Repository[T any] interface {
	Get(ctx context.Context, id int) (T, error)
	List(ctx context.Context) ([]T, error)
	Save(ctx context.Context, item T) error
	Delete(ctx context.Context, id int) error
}
