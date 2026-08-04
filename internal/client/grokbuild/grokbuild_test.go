package grokbuild

import "testing"

func TestIsGrokShellUserAgent(t *testing.T) {
	tests := []struct {
		name string
		ua   string
		want bool
	}{
		{"shell", "grok-shell/0.2.119 (macos; aarch64)", true},
		{"pager", "grok-pager/0.2.119 grok-shell/0.2.119 (macos; aarch64)", true},
		{"case insensitive", "GROK-PAGER/1.0 GROK-SHELL/1.0", true},
		{"ordinary client", "curl/8.7.1", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsGrokShellUserAgent(test.ua); got != test.want {
				t.Fatalf("IsGrokShellUserAgent(%q) = %t, want %t", test.ua, got, test.want)
			}
		})
	}
}

func TestBuildResponse(t *testing.T) {
	response := BuildResponse([]ModelInfo{
		{ID: "grok-4", DisplayName: "Grok 4", ContextLength: 256000, ReasoningLevels: []string{"high"}},
		{ID: "plain-model", ContextLength: 0},
	})

	if response.Object != "list" || len(response.Data) != 2 {
		t.Fatalf("response envelope = %#v", response)
	}
	entry := response.Data[0]
	if entry.ID != "grok-4" || entry.Model != "grok-4" || entry.Name != "Grok 4" {
		t.Fatalf("entry identity = %#v", entry)
	}
	if entry.ContextWindow != 256000 {
		t.Fatalf("entry context = %#v", entry)
	}
	if entry.APIBackend != "responses" || !entry.SupportedInAPI {
		t.Fatalf("entry fixed fields = %#v", entry)
	}
	if len(entry.ReasoningEfforts) != 1 || entry.ReasoningEfforts[0].Value != "high" {
		t.Fatalf("reasoning efforts = %#v", entry.ReasoningEfforts)
	}
	if response.Data[1].Name != "plain-model" || response.Data[1].ContextWindow != 0 || response.Data[1].ReasoningEfforts != nil {
		t.Fatalf("fallback/omitempty mapping = %#v", response.Data[1])
	}
}
