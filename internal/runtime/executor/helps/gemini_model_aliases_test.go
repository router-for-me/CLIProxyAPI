package helps

import (
	"fmt"
	"testing"
)

func TestCanonicalGeminiUpstreamModel(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in, want string
	}{
		{"gemini-3.1-flash-lite-preview", "gemini-3.1-flash-lite"},
		{"  gemini-3.1-flash-lite-preview  ", "gemini-3.1-flash-lite"},
		{"models/gemini-3.1-flash-lite-preview", "gemini-3.1-flash-lite"},
		{"gemini-3.1-flash-lite", "gemini-3.1-flash-lite"},
		{"models/gemini-3.1-flash-lite", "gemini-3.1-flash-lite"},
		{"gemini-2.5-flash", "gemini-2.5-flash"},
		{"models/gemini-2.5-flash", "gemini-2.5-flash"},
		{"", ""},
		{"   ", ""},
	}
	for _, tc := range cases {
		if got := CanonicalGeminiUpstreamModel(tc.in); got != tc.want {
			t.Fatalf("CanonicalGeminiUpstreamModel(%q)=%q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCanonicalGeminiUpstreamModel_usedAsURLSegment(t *testing.T) {
	t.Parallel()
	// Mirror Gemini executor path shape: /v1beta/models/{id}:generateContent
	upstream := CanonicalGeminiUpstreamModel("gemini-3.1-flash-lite-preview")
	gotPath := fmt.Sprintf("/v1beta/models/%s:generateContent", upstream)
	wantPath := "/v1beta/models/gemini-3.1-flash-lite:generateContent"
	if gotPath != wantPath {
		t.Fatalf("path = %q, want %q", gotPath, wantPath)
	}
	// Vertex publishers path shape
	vertexPath := fmt.Sprintf("/v1/projects/p/locations/us-central1/publishers/google/models/%s:generateContent", upstream)
	if vertexPath != "/v1/projects/p/locations/us-central1/publishers/google/models/gemini-3.1-flash-lite:generateContent" {
		t.Fatalf("vertex path = %q", vertexPath)
	}
}
