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

// GreeterFactory exercises the same-package type qualification path:
// its methods reference bar.Greeter via the BARE identifier (Greeter,
// not bar.Greeter) because the interface itself lives in package bar.
// The toolexec generator must qualify these bare idents with the bar
// alias when emitting the mock into the test package.
type GreeterFactory interface {
	Make(prefix string) *Greeter
	WrapAll(gs []*Greeter) []*Greeter
	ByName() map[string]*Greeter
}

// Named is a tiny same-file embed target.
type Named interface {
	Name() string
}

// ReadCloser is an interface whose method set is composed entirely of
// methods promoted from embedded interfaces — one from the stdlib
// (io.Reader) and one declared next door (Named). Used to exercise
// the embedded-interface path in mockgen: the generated backing struct
// must implement Read (from io.Reader, another package) AND Name
// (from Named, same package, same file).
type ReadCloser interface {
	io.Reader
	Named
	Close() error
}

// Base is a generic same-file interface used as an embed target.
// Its type parameter propagates into the promoted method.
type Base[T any] interface {
	Load(id int) T
}

// ListRepo embeds Base[T] with its own U type parameter flowing into
// the embed. Exercises the generic-embed type-parameter flow — Outer's
// U must become Base's T when we instantiate e.g. ListRepo[int].
type ListRepo[U any] interface {
	Base[U]
	List() []U
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
