package plugins_test

import "testing"

func TestGoogleResponsesProtocol(t *testing.T) {
	testVideoResponsesProtocol(t, videoResponsesTestCase{
		pluginKey: "google",
		model:     "veo-3.1-fast-generate-preview",
		requestBody: map[string]any{
			"model": "veo-3.1-fast-generate-preview",
			"input": []any{map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "input_text", "text": "animate this frame"},
				map[string]any{"type": "input_image", "image_url": "data:image/png;base64,aGVsbG8="},
			}}},
			"seconds": 8,
			"size":    "1280x720",
		},
		wantAction: "image_to_video",
		wantRequest: map[string]any{
			"model":    "veo-3.1-fast-generate-preview",
			"prompt":   "animate this frame",
			"images":   []any{"data:image/png;base64,aGVsbG8="},
			"duration": float64(8),
			"size":     "1280x720",
			"metadata": map[string]any{},
		},
		wantUsageKeys:  []string{"resolution", "seconds"},
		wantVendorName: "gemini",
	})
}
