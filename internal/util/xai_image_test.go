package util

import "testing"

func TestXAIImageSizeMapping(t *testing.T) {
	tests := []struct {
		size        string
		aspectRatio string
		resolution  string
		ok          bool
	}{
		{size: "1024x1024", aspectRatio: "1:1", resolution: "1k", ok: true},
		{size: "512x512", aspectRatio: "1:1", resolution: "1k", ok: true},
		{size: "256x256", aspectRatio: "1:1", resolution: "1k", ok: true},
		{size: "1792x1024", aspectRatio: "16:9", resolution: "1k", ok: true},
		{size: "1024x1792", aspectRatio: "9:16", resolution: "1k", ok: true},
		{size: "1536x1024", aspectRatio: "3:2", resolution: "1k", ok: true},
		{size: "1024x1536", aspectRatio: "2:3", resolution: "1k", ok: true},
		{size: "2048x2048", aspectRatio: "1:1", resolution: "2k", ok: true},
		{size: "16:9", aspectRatio: "16:9", resolution: "1k", ok: true},
		{size: "1280x1280", ok: false},
		{size: "1536x1536", ok: false},
		{size: "4096x4096", ok: false},
	}
	for _, tt := range tests {
		aspectRatio, resolution, ok := XAIImageSizeMapping(tt.size)
		if ok != tt.ok || aspectRatio != tt.aspectRatio || resolution != tt.resolution {
			t.Fatalf("XAIImageSizeMapping(%q) = (%q, %q, %v), want (%q, %q, %v)", tt.size, aspectRatio, resolution, ok, tt.aspectRatio, tt.resolution, tt.ok)
		}
	}
}
