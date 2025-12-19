package amp

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestExtractModelFromRequest_JSONBody(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		expected string
	}{
		{
			name:     "standard model field",
			body:     `{"model":"gpt-4","messages":[]}`,
			expected: "gpt-4",
		},
		{
			name:     "claude model",
			body:     `{"model":"claude-sonnet-4-20250514","messages":[]}`,
			expected: "claude-sonnet-4-20250514",
		},
		{
			name:     "empty model field",
			body:     `{"model":"","messages":[]}`,
			expected: "",
		},
		{
			name:     "no model field",
			body:     `{"messages":[]}`,
			expected: "",
		},
		{
			name:     "model field is number",
			body:     `{"model":123,"messages":[]}`,
			expected: "",
		},
		{
			name:     "invalid json",
			body:     `not json`,
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader([]byte(tc.body)))

			result := extractModelFromRequest([]byte(tc.body), c)
			if result != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, result)
			}
		})
	}
}

func TestExtractModelFromRequest_GeminiURLPath(t *testing.T) {
	tests := []struct {
		name     string
		action   string
		expected string
	}{
		{
			name:     "generateContent action",
			action:   "/gemini-2.5-flash:generateContent",
			expected: "gemini-2.5-flash",
		},
		{
			name:     "streamGenerateContent action",
			action:   "/gemini-3-pro-preview:streamGenerateContent",
			expected: "gemini-3-pro-preview",
		},
		{
			name:     "no leading slash",
			action:   "gemini-pro:generateContent",
			expected: "gemini-pro",
		},
		{
			name:     "no colon (invalid)",
			action:   "/gemini-pro",
			expected: "gemini-pro",
		},
		{
			name:     "empty action",
			action:   "",
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/test", nil)
			c.Params = gin.Params{{Key: "action", Value: tc.action}}

			result := extractModelFromRequest([]byte(`{}`), c)
			if result != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, result)
			}
		})
	}
}

func TestExtractModelFromRequest_AmpCLIPath(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		expected string
	}{
		{
			name:     "publishers google models path",
			path:     "/publishers/google/models/gemini-3-pro-preview:streamGenerateContent",
			expected: "gemini-3-pro-preview",
		},
		{
			name:     "simple models path",
			path:     "/models/gemini-2.5-flash:generateContent",
			expected: "gemini-2.5-flash",
		},
		{
			name:     "no colon in path",
			path:     "/publishers/google/models/gemini-pro",
			expected: "",
		},
		{
			name:     "empty path",
			path:     "",
			expected: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/test", nil)
			c.Params = gin.Params{{Key: "path", Value: tc.path}}

			result := extractModelFromRequest([]byte(`{}`), c)
			if result != tc.expected {
				t.Errorf("expected %q, got %q", tc.expected, result)
			}
		})
	}
}

func TestRewriteModelInRequest(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		newModel  string
		wantModel string
	}{
		{
			name:      "replace existing model",
			body:      `{"model":"old-model","messages":[]}`,
			newModel:  "new-model",
			wantModel: "new-model",
		},
		{
			name:      "no model field - unchanged",
			body:      `{"messages":[]}`,
			newModel:  "new-model",
			wantModel: "",
		},
		{
			name:      "empty model - replace",
			body:      `{"model":"","messages":[]}`,
			newModel:  "new-model",
			wantModel: "new-model",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := rewriteModelInRequest([]byte(tc.body), tc.newModel)

			if tc.wantModel == "" {
				if bytes.Contains(result, []byte(`"model"`)) && !bytes.Contains(result, []byte(`"model":""`)) {
					t.Errorf("expected no model field change, but got: %s", result)
				}
				return
			}

			expected := `"model":"` + tc.wantModel + `"`
			if !bytes.Contains(result, []byte(expected)) {
				t.Errorf("expected body to contain %s, got: %s", expected, result)
			}
		})
	}
}
