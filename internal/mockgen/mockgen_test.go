package mockgen

import (
	"strings"
	"testing"
)

func TestGenerateMock_SimpleInterface(t *testing.T) {
	src := []byte(`package bar

type Greeter interface {
	Greet(name string) string
}
`)
	out, err := GenerateMock(src, "Greeter", "bar_test")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Generated mock:\n" + result)

	assertContains(t, result, "package bar_test")
	assertContains(t, result, "type MockGreeter struct")
	assertContains(t, result, "GreetFunc func(name string) string")
	assertContains(t, result, "func (m *MockGreeter) Greet(name string)")
	// Default return should compile (zero value)
	assertContains(t, result, "return")
}

func TestGenerateMock_MultipleMethods(t *testing.T) {
	src := []byte(`package store

type Store interface {
	Get(key string) (string, error)
	Set(key string, value string) error
	Delete(key string) error
	Keys() []string
}
`)
	out, err := GenerateMock(src, "Store", "store_test")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Generated mock:\n" + result)

	assertContains(t, result, "type MockStore struct")
	assertContains(t, result, "GetFunc")
	assertContains(t, result, "func(key string) (string, error)")
	assertContains(t, result, "SetFunc")
	assertContains(t, result, "func(key string, value string) error")
	assertContains(t, result, "DeleteFunc")
	assertContains(t, result, "KeysFunc")
	assertContains(t, result, "func() []string")
	assertContains(t, result, "func (m *MockStore) Get(key string)")
	assertContains(t, result, "func (m *MockStore) Set(key string, value string)")
	assertContains(t, result, "func (m *MockStore) Delete(key string)")
	assertContains(t, result, "func (m *MockStore) Keys()")
}

func TestGenerateMock_NoReturnMethod(t *testing.T) {
	src := []byte(`package bar

type Logger interface {
	Log(msg string)
	LogWith(msg string, fields map[string]any)
}
`)
	out, err := GenerateMock(src, "Logger", "bar_test")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Generated mock:\n" + result)

	assertContains(t, result, "LogFunc")
	assertContains(t, result, "func(msg string)")
	assertContains(t, result, "LogWithFunc")
	assertContains(t, result, "func(msg string, fields map[string]any)")
	assertContains(t, result, "func (m *MockLogger) Log(msg string)")
	assertContains(t, result, "func (m *MockLogger) LogWith(msg string, fields map[string]any)")
}

func TestGenerateMock_WithImportedTypes(t *testing.T) {
	src := []byte(`package bar

import "io"

type Processor interface {
	Process(r io.Reader) (int, error)
}
`)
	out, err := GenerateMock(src, "Processor", "bar_test")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Generated mock:\n" + result)

	assertContains(t, result, `"io"`)
	assertContains(t, result, "ProcessFunc func(r io.Reader) (int, error)")
	assertContains(t, result, "func (m *MockProcessor) Process(r io.Reader)")
}

func TestGenerateMock_VariadicMethod(t *testing.T) {
	src := []byte(`package bar

type Formatter interface {
	Format(pattern string, args ...any) string
}
`)
	out, err := GenerateMock(src, "Formatter", "bar_test")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Generated mock:\n" + result)

	assertContains(t, result, "FormatFunc func(pattern string, args ...any) string")
	assertContains(t, result, "args...")
}

func TestGenerateMock_EmptyInterface(t *testing.T) {
	src := []byte(`package bar

type Empty interface{}
`)
	out, err := GenerateMock(src, "Empty", "bar_test")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Generated mock:\n" + result)

	assertContains(t, result, "type MockEmpty struct")
	// No methods should be generated
	assertNotContains(t, result, "Func")
}

func TestGenerateMock_NotAnInterface(t *testing.T) {
	src := []byte(`package bar

type Foo struct{ X int }
`)
	_, err := GenerateMock(src, "Foo", "bar_test")
	if err == nil {
		t.Fatal("expected error for non-interface type, got nil")
	}
	assertContains(t, err.Error(), "not an interface")
}

func TestGenerateMock_NotFound(t *testing.T) {
	src := []byte(`package bar

type Foo interface{ Do() }
`)
	_, err := GenerateMock(src, "Bar", "bar_test")
	if err == nil {
		t.Fatal("expected error for missing interface, got nil")
	}
	assertContains(t, err.Error(), "not found")
}

func TestGenerateMock_UnnamedParams(t *testing.T) {
	src := []byte(`package bar

type Adder interface {
	Add(int, int) int
}
`)
	out, err := GenerateMock(src, "Adder", "bar_test")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Generated mock:\n" + result)

	// Should generate param names for forwarding
	assertContains(t, result, "p0")
	assertContains(t, result, "p1")
}

func TestGenerateMock_MultipleImports(t *testing.T) {
	src := []byte(`package bar

import (
	"context"
	"io"
)

type Service interface {
	Do(ctx context.Context, r io.Reader) error
}
`)
	out, err := GenerateMock(src, "Service", "bar_test")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Generated mock:\n" + result)

	assertContains(t, result, `"context"`)
	assertContains(t, result, `"io"`)
	assertContains(t, result, "context.Context")
	assertContains(t, result, "io.Reader")
}

func TestGenerateMock_ExternalPackagePointerTypes(t *testing.T) {
	src := []byte(`package bar

import (
	"context"
	"io"
	"net/http"
)

type HTTPClient interface {
	Do(ctx context.Context, req *http.Request) (*http.Response, error)
	Upload(ctx context.Context, url string, body io.Reader) (int64, error)
}
`)
	out, err := GenerateMock(src, "HTTPClient", "bar_test")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Generated mock:\n" + result)

	// Imports
	assertContains(t, result, `"context"`)
	assertContains(t, result, `"io"`)
	assertContains(t, result, `"net/http"`)

	// Struct fields preserve pointer types from external packages
	assertContains(t, result, "*http.Request")
	assertContains(t, result, "*http.Response")
	assertContains(t, result, "io.Reader")
	assertContains(t, result, "context.Context")

	// Method signatures
	assertContains(t, result, "func (m *MockHTTPClient) Do(ctx context.Context, req *http.Request)")
	assertContains(t, result, "func (m *MockHTTPClient) Upload(ctx context.Context, url string, body io.Reader)")
}

func TestGenerateMock_OnlyReferencedImportsIncluded(t *testing.T) {
	src := []byte(`package bar

import (
	"context"
	"io"
	"net/http"
)

type Simple interface {
	Run(ctx context.Context) error
}
`)
	out, err := GenerateMock(src, "Simple", "bar_test")
	if err != nil {
		t.Fatal(err)
	}

	result := string(out)
	t.Log("Generated mock:\n" + result)

	// Only context should be imported, not io or net/http
	assertContains(t, result, `"context"`)
	assertNotContains(t, result, `"io"`)
	assertNotContains(t, result, `"net/http"`)
}

// --- helpers ---

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("expected output to contain %q, but it didn't.\nFull output:\n%s", substr, s)
	}
}

func assertNotContains(t *testing.T, s, substr string) {
	t.Helper()
	if strings.Contains(s, substr) {
		t.Errorf("expected output NOT to contain %q, but it did.\nFull output:\n%s", substr, s)
	}
}
