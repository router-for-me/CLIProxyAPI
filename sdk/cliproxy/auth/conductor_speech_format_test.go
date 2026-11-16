package auth

import (
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestRequestToFormatKeepsMediaSources(t *testing.T) {
	cases := []struct {
		source string
		want   string
	}{
		{source: "openai-image", want: "openai-image"},
		{source: "openai-video", want: "openai-video"},
		{source: "openai-speech", want: "openai-speech"},
	}
	for _, tt := range cases {
		t.Run(tt.source, func(t *testing.T) {
			got := requestToFormat("xai", nil, cliproxyexecutor.Request{}, cliproxyexecutor.Options{
				SourceFormat: sdktranslator.FromString(tt.source),
			})
			if got.String() != tt.want {
				t.Fatalf("requestToFormat() = %q, want %q", got, tt.want)
			}
		})
	}

	got := requestToFormat("xai", nil, cliproxyexecutor.Request{}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FormatOpenAI,
	})
	if got != sdktranslator.FormatCodex {
		t.Fatalf("chat requestToFormat() = %q, want codex", got)
	}
}
