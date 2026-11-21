package helps

import (
	"testing"
)

func TestCodexInputTokenizer(t *testing.T) {
	for _, tc := range []struct{ model, want string }{
		{"gpt-6.1-sol", "o200k_base"}, {"gpt-6.1-terra", "o200k_base"},
		{"gpt-6-luna", "o200k_base"}, {"gpt-6-astra", "o200k_base"},
		{"gpt-5.4", "o200k_base"}, {"gpt-4.1-mini", "o200k_base"},
		{"gpt-4.5-preview", "o200k_base"}, {"gpt-4o-mini", "o200k_base"},
		{"chatgpt-4o-latest", "o200k_base"}, {"o1", "o200k_base"},
		{"o3-mini", "o200k_base"}, {"o4-mini-2025-04-16", "o200k_base"},
		{" GPT-4.1 ", "o200k_base"}, {"gpt-4", "cl100k_base"},
		{"gpt-4-turbo", "cl100k_base"}, {"gpt-3.5-turbo", "cl100k_base"},
		{"ft:gpt-4o-2024-08-06:org:custom:id", "o200k_base"},
		{"ft:gpt-4.1-2025-04-14:org:custom:id", "o200k_base"},
		{"ft:gpt-4.1-mini-2025-04-14:org:custom:id", "o200k_base"},
		{"ft:gpt-4.1-nano-2025-04-14:org:custom:id", "o200k_base"},
		{"ft:gpt-4-0613:org:custom:id", "cl100k_base"},
		{"ft:gpt-3.5-turbo-0125:org:custom:id", "cl100k_base"},
		{"davinci-002", "cl100k_base"}, {"babbage-002", "cl100k_base"},
		{"ft:davinci-002:org:custom:id", "cl100k_base"},
		{"ft:babbage-002:org:custom:id", "cl100k_base"},
		{"future-codex-model", "o200k_base"}, {"", "o200k_base"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			codec, err := CodexInputTokenizer(tc.model)
			if err != nil {
				t.Fatal(err)
			}
			if got := codec.GetName(); got != tc.want {
				t.Fatalf("encoding = %q, want %q", got, tc.want)
			}
		})
	}
}
