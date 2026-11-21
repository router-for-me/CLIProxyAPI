package auth

import (
	"net/http"
	"testing"
)

func TestMergeRequestHeadersReplacesDifferentCasing(t *testing.T) {
	tests := []struct {
		name    string
		current http.Header
		updates http.Header
	}{
		{
			name:    "lowercase current uppercase update",
			current: http.Header{"x-session-id": {"old"}},
			updates: http.Header{"X-Session-ID": {"new"}},
		},
		{
			name:    "uppercase current lowercase update",
			current: http.Header{"X-Session-ID": {"old"}},
			updates: http.Header{"x-session-id": {"new"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeRequestHeaders(tt.current, tt.updates, nil)
			if len(got) != 1 {
				t.Fatalf("mergeRequestHeaders() returned %d headers, want 1: %#v", len(got), got)
			}
			if values := got.Values("X-Session-Id"); len(values) != 1 || values[0] != "new" {
				t.Fatalf("X-Session-Id values = %#v, want [new]", values)
			}
		})
	}
}
