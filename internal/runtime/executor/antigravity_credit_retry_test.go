package executor

import (
	"net/http"
	"testing"

	"github.com/tidwall/gjson"
)

func TestAntigravityIsFreeQuotaExhausted(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
		want       bool
	}{
		{
			name:       "429 with RESOURCE_EXHAUSTED",
			statusCode: http.StatusTooManyRequests,
			body:       `{"error":{"code":429,"message":"Resource has been exhausted","status":"RESOURCE_EXHAUSTED"}}`,
			want:       true,
		},
		{
			name:       "429 with quota message",
			statusCode: http.StatusTooManyRequests,
			body:       `{"error":{"code":429,"message":"Quota exceeded for the day"}}`,
			want:       true,
		},
		{
			name:       "429 without quota indicators",
			statusCode: http.StatusTooManyRequests,
			body:       `{"error":{"code":429,"message":"rate limit exceeded"}}`,
			want:       false,
		},
		{
			name:       "503 no capacity is not quota",
			statusCode: http.StatusServiceUnavailable,
			body:       `no capacity available`,
			want:       false,
		},
		{
			name:       "200 success is not quota",
			statusCode: http.StatusOK,
			body:       `{"candidates":[]}`,
			want:       false,
		},
		{
			name:       "429 empty body",
			statusCode: http.StatusTooManyRequests,
			body:       "",
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := antigravityIsFreeQuotaExhausted(tt.statusCode, []byte(tt.body))
			if got != tt.want {
				t.Errorf("antigravityIsFreeQuotaExhausted(%d, %q) = %v, want %v", tt.statusCode, tt.body, got, tt.want)
			}
		})
	}
}

func TestAntigravityInjectCreditTypes(t *testing.T) {
	payload := []byte(`{"model":"gemini-2.5-pro","project":"my-project","request":{"contents":[]}}`)
	result := antigravityInjectCreditTypes(payload)

	creditTypes := gjson.GetBytes(result, "enabledCreditTypes")
	if !creditTypes.Exists() {
		t.Fatal("enabledCreditTypes field not found in result")
	}
	if !creditTypes.IsArray() {
		t.Fatalf("enabledCreditTypes should be array, got: %s", creditTypes.Type)
	}
	arr := creditTypes.Array()
	if len(arr) != 1 {
		t.Fatalf("expected 1 credit type, got %d", len(arr))
	}
	if arr[0].String() != "GOOGLE_ONE_AI" {
		t.Errorf("expected GOOGLE_ONE_AI, got %s", arr[0].String())
	}

	// Verify original fields are preserved
	if gjson.GetBytes(result, "model").String() != "gemini-2.5-pro" {
		t.Error("model field was corrupted")
	}
	if gjson.GetBytes(result, "project").String() != "my-project" {
		t.Error("project field was corrupted")
	}
}

func TestAntigravityInjectCreditTypes_Idempotent(t *testing.T) {
	payload := []byte(`{"model":"test"}`)
	first := antigravityInjectCreditTypes(payload)
	second := antigravityInjectCreditTypes(first)

	arr := gjson.GetBytes(second, "enabledCreditTypes").Array()
	if len(arr) != 1 {
		t.Errorf("double injection should still yield 1 element, got %d", len(arr))
	}
}
