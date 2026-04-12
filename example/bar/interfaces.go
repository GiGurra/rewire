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
