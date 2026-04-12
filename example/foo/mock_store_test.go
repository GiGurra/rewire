package foo

type MockStore struct {
	GetFunc    func(key string) (string, error)
	SetFunc    func(key string, value string) error
	DeleteFunc func(key string) error
}

func (m *MockStore) Get(key string) (_r0 string, _r1 error) {
	if m.GetFunc != nil {
		return m.GetFunc(key)
	}
	return
}

func (m *MockStore) Set(key string, value string) (_r0 error) {
	if m.SetFunc != nil {
		return m.SetFunc(key, value)
	}
	return
}

func (m *MockStore) Delete(key string) (_r0 error) {
	if m.DeleteFunc != nil {
		return m.DeleteFunc(key)
	}
	return
}
