package foo

type MockLogger struct {
	LogFunc  func(msg string)
	LogfFunc func(format string, args ...any)
}

func (m *MockLogger) Log(msg string) {
	if m.LogFunc != nil {
		m.LogFunc(msg)
	}
}

func (m *MockLogger) Logf(format string, args ...any) {
	if m.LogfFunc != nil {
		m.LogfFunc(format, args...)
	}
}
