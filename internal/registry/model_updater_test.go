package registry

import (
	"strings"
	"testing"
)

func TestValidateModelsCatalog_AllowsMissingOptionalSections(t *testing.T) {
	catalog := &staticModelsJSON{
		Claude:      []*ModelInfo{{ID: "claude-test"}},
		Gemini:      []*ModelInfo{{ID: "gemini-test"}},
		Vertex:      []*ModelInfo{{ID: "vertex-test"}},
		GeminiCLI:   []*ModelInfo{{ID: "gemini-cli-test"}},
		AIStudio:    []*ModelInfo{{ID: "aistudio-test"}},
		CodexFree:   []*ModelInfo{{ID: "codex-free-test"}},
		CodexTeam:   []*ModelInfo{{ID: "codex-team-test"}},
		CodexPlus:   []*ModelInfo{{ID: "codex-plus-test"}},
		CodexPro:    []*ModelInfo{{ID: "codex-pro-test"}},
		Kimi:        []*ModelInfo{{ID: "kimi-test"}},
		Antigravity: []*ModelInfo{{ID: "antigravity-test"}},
	}

	if err := validateModelsCatalog(catalog); err != nil {
		t.Fatalf("validateModelsCatalog() error = %v, want nil", err)
	}
}

func TestValidateModelsCatalog_RejectsDuplicateIDsInOptionalSection(t *testing.T) {
	catalog := &staticModelsJSON{
		Claude:      []*ModelInfo{{ID: "claude-test"}},
		Gemini:      []*ModelInfo{{ID: "gemini-test"}},
		Vertex:      []*ModelInfo{{ID: "vertex-test"}},
		GeminiCLI:   []*ModelInfo{{ID: "gemini-cli-test"}},
		AIStudio:    []*ModelInfo{{ID: "aistudio-test"}},
		CodexFree:   []*ModelInfo{{ID: "codex-free-test"}},
		CodexTeam:   []*ModelInfo{{ID: "codex-team-test"}},
		CodexPlus:   []*ModelInfo{{ID: "codex-plus-test"}},
		CodexPro:    []*ModelInfo{{ID: "codex-pro-test"}},
		Qwen:        []*ModelInfo{{ID: "dup"}, {ID: "dup"}},
		Kimi:        []*ModelInfo{{ID: "kimi-test"}},
		Antigravity: []*ModelInfo{{ID: "antigravity-test"}},
	}

	err := validateModelsCatalog(catalog)
	if err == nil {
		t.Fatal("validateModelsCatalog() error = nil, want duplicate-id error")
	}
	if !strings.Contains(err.Error(), `qwen contains duplicate model id "dup"`) {
		t.Fatalf("validateModelsCatalog() error = %v, want duplicate qwen id error", err)
	}
}
