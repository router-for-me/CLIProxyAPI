package testutil

import (
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

// FakeHandlerRecorder records handler invocations for testing.
type FakeHandlerRecorder struct {
	Called        bool
	CallCount     int
	RequestBody   []byte
	RequestHeader http.Header
	ContextKeys   map[string]interface{}
	ResponseStatus int
	ResponseBody  []byte
}

// NewFakeHandlerRecorder creates a new fake handler recorder.
func NewFakeHandlerRecorder() *FakeHandlerRecorder {
	return &FakeHandlerRecorder{
		ContextKeys:    make(map[string]interface{}),
		ResponseStatus: http.StatusOK,
		ResponseBody:   []byte(`{"status":"handled"}`),
	}
}

// GinHandler returns a gin.HandlerFunc that records the invocation.
func (f *FakeHandlerRecorder) GinHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		f.record(c)
		c.Data(f.ResponseStatus, "application/json", f.ResponseBody)
	}
}

// GinHandlerWithModel returns a gin.HandlerFunc that records the invocation and returns the model from context.
// Useful for testing response rewriting in model mapping scenarios.
func (f *FakeHandlerRecorder) GinHandlerWithModel() gin.HandlerFunc {
	return func(c *gin.Context) {
		f.record(c)
		// Return a response with the model field that would be in the actual API response
		// If ResponseBody was explicitly set (not default), use that; otherwise generate from context
		var body []byte
		if mappedModel, exists := c.Get("mapped_model"); exists {
			body = []byte(`{"model":"` + mappedModel.(string) + `","status":"handled"}`)
		} else {
			body = f.ResponseBody
		}
		c.Data(f.ResponseStatus, "application/json", body)
	}
}

// HTTPHandler returns an http.HandlerFunc that records the invocation.
func (f *FakeHandlerRecorder) HTTPHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.Called = true
		f.CallCount++
		f.RequestBody = body
		f.RequestHeader = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.ResponseStatus)
		w.Write(f.ResponseBody)
	}
}

// record captures the request details from gin context.
func (f *FakeHandlerRecorder) record(c *gin.Context) {
	f.Called = true
	f.CallCount++

	body, _ := io.ReadAll(c.Request.Body)
	f.RequestBody = body
	f.RequestHeader = c.Request.Header.Clone()

	// Capture common context keys used by routing
	if val, exists := c.Get("mapped_model"); exists {
		f.ContextKeys["mapped_model"] = val
	}
	if val, exists := c.Get("fallback_models"); exists {
		f.ContextKeys["fallback_models"] = val
	}
	if val, exists := c.Get("route_type"); exists {
		f.ContextKeys["route_type"] = val
	}
}

// Reset clears the recorder state.
func (f *FakeHandlerRecorder) Reset() {
	f.Called = false
	f.CallCount = 0
	f.RequestBody = nil
	f.RequestHeader = nil
	f.ContextKeys = make(map[string]interface{})
}

// GetContextKey returns a captured context key value.
func (f *FakeHandlerRecorder) GetContextKey(key string) (interface{}, bool) {
	val, ok := f.ContextKeys[key]
	return val, ok
}

// WasCalled returns true if the handler was called.
func (f *FakeHandlerRecorder) WasCalled() bool {
	return f.Called
}

// GetCallCount returns the number of times the handler was called.
func (f *FakeHandlerRecorder) GetCallCount() int {
	return f.CallCount
}
