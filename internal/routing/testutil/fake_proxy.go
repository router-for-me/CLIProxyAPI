package testutil

import (
	"io"
	"net/http"
	"net/http/httptest"
)

// CloseNotifierRecorder wraps httptest.ResponseRecorder with CloseNotify support.
// This is needed because ReverseProxy requires http.CloseNotifier.
type CloseNotifierRecorder struct {
	*httptest.ResponseRecorder
	closeChan chan bool
}

// NewCloseNotifierRecorder creates a ResponseRecorder that implements CloseNotifier.
func NewCloseNotifierRecorder() *CloseNotifierRecorder {
	return &CloseNotifierRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		closeChan:        make(chan bool, 1),
	}
}

// CloseNotify implements http.CloseNotifier.
func (c *CloseNotifierRecorder) CloseNotify() <-chan bool {
	return c.closeChan
}

// FakeProxyRecorder records proxy invocations for testing.
type FakeProxyRecorder struct {
	Called         bool
	CallCount      int
	RequestBody    []byte
	RequestHeaders http.Header
	ResponseStatus int
	ResponseBody   []byte
}

// NewFakeProxyRecorder creates a new fake proxy recorder.
func NewFakeProxyRecorder() *FakeProxyRecorder {
	return &FakeProxyRecorder{
		ResponseStatus: http.StatusOK,
		ResponseBody:   []byte(`{"status":"proxied"}`),
	}
}

// ServeHTTP implements http.Handler to act as a reverse proxy.
func (f *FakeProxyRecorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.Called = true
	f.CallCount++
	f.RequestHeaders = r.Header.Clone()

	body, err := io.ReadAll(r.Body)
	if err == nil {
		f.RequestBody = body
	}

	w.WriteHeader(f.ResponseStatus)
	w.Write(f.ResponseBody)
}

// GetCallCount returns the number of times the proxy was called.
func (f *FakeProxyRecorder) GetCallCount() int {
	return f.CallCount
}

// Reset clears the recorder state.
func (f *FakeProxyRecorder) Reset() {
	f.Called = false
	f.CallCount = 0
	f.RequestBody = nil
	f.RequestHeaders = nil
}

// ToHandler returns the recorder as an http.Handler for use with httptest.
func (f *FakeProxyRecorder) ToHandler() http.Handler {
	return http.HandlerFunc(f.ServeHTTP)
}

// CreateTestServer creates an httptest server with this fake proxy.
func (f *FakeProxyRecorder) CreateTestServer() *httptest.Server {
	return httptest.NewServer(f.ToHandler())
}
