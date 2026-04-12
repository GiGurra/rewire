package foo

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/GiGurra/rewire/example/bar"
)

// GreetAll greets each name using the provided greeter.
func GreetAll(g bar.GreeterIface, names []string) []string {
	var results []string
	for _, name := range names {
		results = append(results, g.Greet(name))
	}
	return results
}

// GetOrDefault returns the value for key, or defaultVal if not found.
func GetOrDefault(s bar.Store, key, defaultVal string) string {
	val, err := s.Get(key)
	if err != nil {
		return defaultVal
	}
	return val
}

// MigrateKey moves a value from oldKey to newKey, deleting the old key.
func MigrateKey(s bar.Store, oldKey, newKey string) error {
	val, err := s.Get(oldKey)
	if err != nil {
		return fmt.Errorf("reading old key: %w", err)
	}
	if err := s.Set(newKey, val); err != nil {
		return fmt.Errorf("writing new key: %w", err)
	}
	if err := s.Delete(oldKey); err != nil {
		return fmt.Errorf("deleting old key: %w", err)
	}
	return nil
}

// LogAndGreet logs the greeting and returns it.
func LogAndGreet(l bar.Logger, g bar.GreeterIface, name string) string {
	result := g.Greet(name)
	l.Logf("greeted %s: %s", name, result)
	return result
}

// FetchBody uses an HTTPClient to GET a URL and return the body as a string.
func FetchBody(ctx context.Context, client bar.HTTPClient, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("creating request: %w", err)
	}
	resp, err := client.Do(ctx, req)
	if err != nil {
		return "", fmt.Errorf("executing request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("reading body: %w", err)
	}
	return string(body), nil
}

// UploadString uploads a string body to a URL and returns bytes written.
func UploadString(ctx context.Context, client bar.HTTPClient, url, content string) (int64, error) {
	return client.Upload(ctx, url, strings.NewReader(content))
}
