package foo

import (
	"fmt"

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
