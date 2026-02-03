package routing

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestModelExtractor_ExtractFromJSONBody(t *testing.T) {
	extractor := NewModelExtractor()

	tests := []struct {
		name     string
		body     []byte
		want     string
		wantErr  bool
	}{
		{
			name: "extract from JSON body with model field",
			body: []byte(`{"model":"gpt-4.1"}`),
			want: "gpt-4.1",
		},
		{
			name: "extract claude model from JSON body",
			body: []byte(`{"model":"claude-3-5-sonnet-20241022"}`),
			want: "claude-3-5-sonnet-20241022",
		},
		{
			name: "extract with additional fields",
			body: []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hello"}]}`),
			want: "gpt-4",
		},
		{
			name: "empty body returns empty",
			body: []byte{},
			want: "",
		},
		{
			name: "no model field returns empty",
			body: []byte(`{"messages":[]}`),
			want: "",
		},
		{
			name: "model is not string returns empty",
			body: []byte(`{"model":123}`),
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractor.Extract(tt.body, nil)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestModelExtractor_ExtractFromGeminiActionParam(t *testing.T) {
	extractor := NewModelExtractor()

	tests := []struct {
		name      string
		body      []byte
		ginParams map[string]string
		want      string
	}{
		{
			name:      "extract from action parameter - gemini-pro",
			body:      []byte(`{}`),
			ginParams: map[string]string{"action": "gemini-pro:generateContent"},
			want:      "gemini-pro",
		},
		{
			name:      "extract from action parameter - gemini-ultra",
			body:      []byte(`{}`),
			ginParams: map[string]string{"action": "gemini-ultra:chat"},
			want:      "gemini-ultra",
		},
		{
			name:      "empty action returns empty",
			body:      []byte(`{}`),
			ginParams: map[string]string{"action": ""},
			want:      "",
		},
		{
			name:      "action without colon returns full value",
			body:      []byte(`{}`),
			ginParams: map[string]string{"action": "gemini-model"},
			want:      "gemini-model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractor.Extract(tt.body, tt.ginParams)
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestModelExtractor_ExtractFromGeminiV1Beta1Path(t *testing.T) {
	extractor := NewModelExtractor()

	tests := []struct {
		name      string
		body      []byte
		ginParams map[string]string
		want      string
	}{
		{
			name:      "extract from v1beta1 path - gemini-3-pro",
			body:      []byte(`{}`),
			ginParams: map[string]string{"path": "/publishers/google/models/gemini-3-pro:streamGenerateContent"},
			want:      "gemini-3-pro",
		},
		{
			name:      "extract from v1beta1 path with preview",
			body:      []byte(`{}`),
			ginParams: map[string]string{"path": "/publishers/google/models/gemini-3-pro-preview:generateContent"},
			want:      "gemini-3-pro-preview",
		},
		{
			name:      "path without models segment returns empty",
			body:      []byte(`{}`),
			ginParams: map[string]string{"path": "/publishers/google/gemini-3-pro:streamGenerateContent"},
			want:      "",
		},
		{
			name:      "empty path returns empty",
			body:      []byte(`{}`),
			ginParams: map[string]string{"path": ""},
			want:      "",
		},
		{
			name:      "path with /models/ but no colon returns empty",
			body:      []byte(`{}`),
			ginParams: map[string]string{"path": "/publishers/google/models/gemini-3-pro"},
			want:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractor.Extract(tt.body, tt.ginParams)
			assert.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestModelExtractor_ExtractPriority(t *testing.T) {
	extractor := NewModelExtractor()

	// JSON body takes priority over gin params
	t.Run("JSON body takes priority over action param", func(t *testing.T) {
		body := []byte(`{"model":"gpt-4"}`)
		params := map[string]string{"action": "gemini-pro:generateContent"}
		got, err := extractor.Extract(body, params)
		assert.NoError(t, err)
		assert.Equal(t, "gpt-4", got)
	})

	// Action param takes priority over path param
	t.Run("action param takes priority over path param", func(t *testing.T) {
		body := []byte(`{}`)
		params := map[string]string{
			"action": "gemini-action:generate",
			"path":   "/publishers/google/models/gemini-path:streamGenerateContent",
		}
		got, err := extractor.Extract(body, params)
		assert.NoError(t, err)
		assert.Equal(t, "gemini-action", got)
	})
}

func TestModelExtractor_NoModelFound(t *testing.T) {
	extractor := NewModelExtractor()

	tests := []struct {
		name      string
		body      []byte
		ginParams map[string]string
	}{
		{
			name:      "empty body and no params",
			body:      []byte{},
			ginParams: nil,
		},
		{
			name:      "body without model and no params",
			body:      []byte(`{"messages":[]}`),
			ginParams: map[string]string{},
		},
		{
			name:      "irrelevant params only",
			body:      []byte(`{}`),
			ginParams: map[string]string{"other": "value"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractor.Extract(tt.body, tt.ginParams)
			assert.NoError(t, err)
			assert.Empty(t, got)
		})
	}
}
