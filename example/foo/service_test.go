package foo

import (
	"errors"
	"fmt"
	"testing"
)

// --- GreeterIface mock tests ---

func TestGreetAll(t *testing.T) {
	mock := &MockGreeterIface{
		GreetFunc: func(name string) string {
			return "Hi, " + name
		},
	}

	got := GreetAll(mock, []string{"Alice", "Bob", "Charlie"})
	want := []string{"Hi, Alice", "Hi, Bob", "Hi, Charlie"}

	if len(got) != len(want) {
		t.Fatalf("got %d results, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestGreetAll_Empty(t *testing.T) {
	callCount := 0
	mock := &MockGreeterIface{
		GreetFunc: func(name string) string {
			callCount++
			return ""
		},
	}

	got := GreetAll(mock, nil)
	if len(got) != 0 {
		t.Errorf("expected empty result, got %v", got)
	}
	if callCount != 0 {
		t.Errorf("expected 0 calls, got %d", callCount)
	}
}

func TestGreetAll_CallTracking(t *testing.T) {
	var calls []string
	mock := &MockGreeterIface{
		GreetFunc: func(name string) string {
			calls = append(calls, name)
			return "Hello"
		},
	}

	GreetAll(mock, []string{"A", "B", "C"})

	if len(calls) != 3 {
		t.Fatalf("expected 3 calls, got %d", len(calls))
	}
	if calls[0] != "A" || calls[1] != "B" || calls[2] != "C" {
		t.Errorf("unexpected call order: %v", calls)
	}
}

// --- Store mock tests ---

func TestGetOrDefault_Found(t *testing.T) {
	mock := &MockStore{
		GetFunc: func(key string) (string, error) {
			if key == "name" {
				return "Alice", nil
			}
			return "", errors.New("not found")
		},
	}

	got := GetOrDefault(mock, "name", "default")
	if got != "Alice" {
		t.Errorf("got %q, want %q", got, "Alice")
	}
}

func TestGetOrDefault_NotFound(t *testing.T) {
	mock := &MockStore{
		GetFunc: func(key string) (string, error) {
			return "", errors.New("not found")
		},
	}

	got := GetOrDefault(mock, "missing", "fallback")
	if got != "fallback" {
		t.Errorf("got %q, want %q", got, "fallback")
	}
}

func TestGetOrDefault_UnsetMockReturnsZero(t *testing.T) {
	// GetFunc is nil — should return zero values ("", nil)
	mock := &MockStore{}

	got := GetOrDefault(mock, "key", "fallback")
	// Get returns ("", nil), so no error → returns ""
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
}

func TestMigrateKey_Success(t *testing.T) {
	data := map[string]string{"old": "value"}

	mock := &MockStore{
		GetFunc: func(key string) (string, error) {
			v, ok := data[key]
			if !ok {
				return "", errors.New("not found")
			}
			return v, nil
		},
		SetFunc: func(key, value string) error {
			data[key] = value
			return nil
		},
		DeleteFunc: func(key string) error {
			delete(data, key)
			return nil
		},
	}

	err := MigrateKey(mock, "old", "new")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, ok := data["old"]; ok {
		t.Error("old key should be deleted")
	}
	if data["new"] != "value" {
		t.Errorf("new key = %q, want %q", data["new"], "value")
	}
}

func TestMigrateKey_GetFails(t *testing.T) {
	mock := &MockStore{
		GetFunc: func(key string) (string, error) {
			return "", errors.New("db down")
		},
	}

	err := MigrateKey(mock, "old", "new")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, errors.Unwrap(err)) {
		// Just check it wraps the message
	}
}

func TestMigrateKey_SetFails(t *testing.T) {
	mock := &MockStore{
		GetFunc: func(key string) (string, error) {
			return "value", nil
		},
		SetFunc: func(key, value string) error {
			return errors.New("read-only")
		},
	}

	err := MigrateKey(mock, "old", "new")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestMigrateKey_DeleteFails(t *testing.T) {
	mock := &MockStore{
		GetFunc: func(key string) (string, error) {
			return "value", nil
		},
		SetFunc: func(key, value string) error {
			return nil
		},
		DeleteFunc: func(key string) error {
			return errors.New("permission denied")
		},
	}

	err := MigrateKey(mock, "old", "new")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

// --- Logger mock tests ---

func TestLogAndGreet(t *testing.T) {
	var logged []string
	logger := &MockLogger{
		LogfFunc: func(format string, args ...any) {
			logged = append(logged, fmt.Sprintf(format, args...))
		},
	}
	greeter := &MockGreeterIface{
		GreetFunc: func(name string) string {
			return "Hey, " + name + "!"
		},
	}

	got := LogAndGreet(logger, greeter, "Alice")

	if got != "Hey, Alice!" {
		t.Errorf("got %q, want %q", got, "Hey, Alice!")
	}
	if len(logged) != 1 {
		t.Fatalf("expected 1 log entry, got %d", len(logged))
	}
	if logged[0] != "greeted Alice: Hey, Alice!" {
		t.Errorf("logged %q, want %q", logged[0], "greeted Alice: Hey, Alice!")
	}
}

func TestLogAndGreet_UnsetLogger(t *testing.T) {
	// Logger with no funcs set — should not panic
	logger := &MockLogger{}
	greeter := &MockGreeterIface{
		GreetFunc: func(name string) string { return "hi" },
	}

	got := LogAndGreet(logger, greeter, "Bob")
	if got != "hi" {
		t.Errorf("got %q, want %q", got, "hi")
	}
}

func TestLogger_LogCallTracking(t *testing.T) {
	var messages []string
	logger := &MockLogger{
		LogFunc: func(msg string) {
			messages = append(messages, msg)
		},
	}

	logger.Log("first")
	logger.Log("second")
	logger.Log("third")

	if len(messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(messages))
	}
	if messages[0] != "first" || messages[1] != "second" || messages[2] != "third" {
		t.Errorf("unexpected messages: %v", messages)
	}
}
