package executor

import (
	"testing"
)

// ---------------------------------------------------------------------------
// isCapabilityError
// ---------------------------------------------------------------------------

func TestIsCapabilityError_StatusCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		body       string
		want       bool
	}{
		{name: "404 is capability error", statusCode: 404, body: "", want: true},
		{name: "405 is capability error", statusCode: 405, body: "", want: true},
		{name: "501 is capability error", statusCode: 501, body: "", want: true},
		{name: "400 is not capability error", statusCode: 400, body: "", want: false},
		{name: "401 is not capability error", statusCode: 401, body: "", want: false},
		{name: "403 is not capability error", statusCode: 403, body: "", want: false},
		{name: "429 is not capability error", statusCode: 429, body: "", want: false},
		{name: "500 is not capability error (no keywords)", statusCode: 500, body: `{"error":"internal"}`, want: false},
		{name: "502 is not capability error", statusCode: 502, body: "", want: false},
		{name: "503 is not capability error", statusCode: 503, body: "", want: false},
		{name: "200 is not capability error", statusCode: 200, body: "", want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isCapabilityError(tt.statusCode, []byte(tt.body))
			if got != tt.want {
				t.Errorf("isCapabilityError(%d, %q) = %v, want %v", tt.statusCode, tt.body, got, tt.want)
			}
		})
	}
}

func TestIsCapabilityError_BodyKeywords(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		body       string
		want       bool
	}{
		{
			name:       "500 + convert_request_failed",
			statusCode: 500,
			body:       `{"error":{"message":"convert_request_failed","type":"server_error"}}`,
			want:       true,
		},
		{
			name:       "500 + not implemented",
			statusCode: 500,
			body:       `{"error":{"message":"Not Implemented","type":"invalid_request_error"}}`,
			want:       true,
		},
		{
			name:       "500 + endpoint not found",
			statusCode: 500,
			body:       `{"error":{"message":"Endpoint not found","type":"not_found"}}`,
			want:       true,
		},
		{
			name:       "200 + convert_request_failed (should still match)",
			statusCode: 200,
			body:       `{"error":"convert_request_failed"}`,
			want:       true,
		},
		{
			name:       "500 + unrelated body",
			statusCode: 500,
			body:       `{"error":{"message":"rate limit exceeded","type":"rate_limit"}}`,
			want:       false,
		},
		{
			name:       "empty body with non-matching status",
			statusCode: 500,
			body:       "",
			want:       false,
		},
		{
			name:       "nil body",
			statusCode: 500,
			body:       "",
			want:       false,
		},
		{
			name:       "case insensitive keyword match",
			statusCode: 500,
			body:       `{"error":"CONVERT_REQUEST_FAILED"}`,
			want:       true,
		},
		{
			name:       "keyword as substring",
			statusCode: 500,
			body:       `{"detail":"this endpoint not found in registry"}`,
			want:       true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var body []byte
			if tt.body != "" {
				body = []byte(tt.body)
			}
			got := isCapabilityError(tt.statusCode, body)
			if got != tt.want {
				t.Errorf("isCapabilityError(%d, %q) = %v, want %v", tt.statusCode, tt.body, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ResponsesMode String()
// ---------------------------------------------------------------------------

func TestResponsesModeString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		mode ResponsesMode
		want string
	}{
		{ResponsesModeUnknown, "unknown"},
		{ResponsesModeNative, "native"},
		{ResponsesModeChatFallback, "chat_fallback"},
		{ResponsesMode(99), "unknown"},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()
			if got := tt.mode.String(); got != tt.want {
				t.Errorf("ResponsesMode(%d).String() = %q, want %q", tt.mode, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ResponsesCapabilityResolver
// ---------------------------------------------------------------------------

func TestResponsesCapabilityResolver_UnknownByDefault(t *testing.T) {
	t.Parallel()
	r := NewResponsesCapabilityResolver()
	if mode := r.Resolve("nonexistent"); mode != ResponsesModeUnknown {
		t.Fatalf("expected Unknown for missing key, got %v", mode)
	}
}

func TestResponsesCapabilityResolver_SetAndResolve(t *testing.T) {
	t.Parallel()
	r := NewResponsesCapabilityResolver()

	r.Set("auth-1", ResponsesModeNative)
	if mode := r.Resolve("auth-1"); mode != ResponsesModeNative {
		t.Fatalf("expected Native, got %v", mode)
	}

	r.Set("auth-2", ResponsesModeChatFallback)
	if mode := r.Resolve("auth-2"); mode != ResponsesModeChatFallback {
		t.Fatalf("expected ChatFallback, got %v", mode)
	}
}

func TestResponsesCapabilityResolver_Invalidate(t *testing.T) {
	t.Parallel()
	r := NewResponsesCapabilityResolver()

	r.Set("auth-1", ResponsesModeNative)
	r.Invalidate("auth-1")
	if mode := r.Resolve("auth-1"); mode != ResponsesModeUnknown {
		t.Fatalf("expected Unknown after invalidation, got %v", mode)
	}
}

func TestResponsesCapabilityResolver_Overwrite(t *testing.T) {
	t.Parallel()
	r := NewResponsesCapabilityResolver()

	r.Set("auth-1", ResponsesModeNative)
	r.Set("auth-1", ResponsesModeChatFallback)
	if mode := r.Resolve("auth-1"); mode != ResponsesModeChatFallback {
		t.Fatalf("expected ChatFallback after overwrite, got %v", mode)
	}
}

func TestResponsesCapabilityResolver_ConcurrentAccess(t *testing.T) {
	t.Parallel()
	r := NewResponsesCapabilityResolver()

	// Concurrent set and resolve to detect data races with -race flag.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			r.Set("auth-concurrent", ResponsesModeNative)
		}
	}()
	for i := 0; i < 100; i++ {
		_ = r.Resolve("auth-concurrent")
	}
	<-done
}
