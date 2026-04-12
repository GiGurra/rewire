package foo

import (
	"context"
	"io"
	"net/http"
)

type MockHTTPClient struct {
	DoFunc     func(ctx context.Context, req *http.Request) (*http.Response, error)
	UploadFunc func(ctx context.Context, url string, body io.Reader) (int64, error)
}

func (m *MockHTTPClient) Do(ctx context.Context, req *http.Request) (_r0 *http.Response, _r1 error) {
	if m.DoFunc != nil {
		return m.DoFunc(ctx, req)
	}
	return
}

func (m *MockHTTPClient) Upload(ctx context.Context, url string, body io.Reader) (_r0 int64, _r1 error) {
	if m.UploadFunc != nil {
		return m.UploadFunc(ctx, url, body)
	}
	return
}
