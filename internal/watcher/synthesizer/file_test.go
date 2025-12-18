package synthesizer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestNewFileSynthesizer(t *testing.T) {
	synth := NewFileSynthesizer()
	if synth == nil {
		t.Fatal("expected non-nil synthesizer")
	}
}

func TestFileSynthesizer_Synthesize_NilContext(t *testing.T) {
	synth := NewFileSynthesizer()
	auths, err := synth.Synthesize(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 0 {
		t.Fatalf("expected empty auths, got %d", len(auths))
	}
}

func TestFileSynthesizer_Synthesize_EmptyAuthDir(t *testing.T) {
	synth := NewFileSynthesizer()
	ctx := &SynthesisContext{
		Config:      &config.Config{},
		AuthDir:     "",
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}
	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 0 {
		t.Fatalf("expected empty auths, got %d", len(auths))
	}
}

func TestFileSynthesizer_Synthesize_NonExistentDir(t *testing.T) {
	synth := NewFileSynthesizer()
	ctx := &SynthesisContext{
		Config:      &config.Config{},
		AuthDir:     "/non/existent/path",
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}
	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 0 {
		t.Fatalf("expected empty auths, got %d", len(auths))
	}
}

func TestFileSynthesizer_Synthesize_ValidAuthFile(t *testing.T) {
	tempDir := t.TempDir()

	// Create a valid auth file
	authData := map[string]any{
		"type":      "claude",
		"email":     "test@example.com",
		"proxy_url": "http://proxy.local",
		"prefix":    "test-prefix",
	}
	data, _ := json.Marshal(authData)
	err := os.WriteFile(filepath.Join(tempDir, "claude-auth.json"), data, 0644)
	if err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}

	synth := NewFileSynthesizer()
	ctx := &SynthesisContext{
		Config:      &config.Config{},
		AuthDir:     tempDir,
		Now:         time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth, got %d", len(auths))
	}

	if auths[0].Provider != "claude" {
		t.Errorf("expected provider claude, got %s", auths[0].Provider)
	}
	if auths[0].Label != "test@example.com" {
		t.Errorf("expected label test@example.com, got %s", auths[0].Label)
	}
	if auths[0].Prefix != "test-prefix" {
		t.Errorf("expected prefix test-prefix, got %s", auths[0].Prefix)
	}
	if auths[0].ProxyURL != "http://proxy.local" {
		t.Errorf("expected proxy_url http://proxy.local, got %s", auths[0].ProxyURL)
	}
	if auths[0].Status != coreauth.StatusActive {
		t.Errorf("expected status active, got %s", auths[0].Status)
	}
}

func TestFileSynthesizer_Synthesize_GeminiProviderMapping(t *testing.T) {
	tempDir := t.TempDir()

	// Gemini type should be mapped to gemini-cli
	authData := map[string]any{
		"type":  "gemini",
		"email": "gemini@example.com",
	}
	data, _ := json.Marshal(authData)
	err := os.WriteFile(filepath.Join(tempDir, "gemini-auth.json"), data, 0644)
	if err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}

	synth := NewFileSynthesizer()
	ctx := &SynthesisContext{
		Config:      &config.Config{},
		AuthDir:     tempDir,
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth, got %d", len(auths))
	}

	if auths[0].Provider != "gemini-cli" {
		t.Errorf("gemini should be mapped to gemini-cli, got %s", auths[0].Provider)
	}
}

func TestFileSynthesizer_Synthesize_SkipsInvalidFiles(t *testing.T) {
	tempDir := t.TempDir()

	// Create various invalid files
	_ = os.WriteFile(filepath.Join(tempDir, "not-json.txt"), []byte("text content"), 0644)
	_ = os.WriteFile(filepath.Join(tempDir, "invalid.json"), []byte("not valid json"), 0644)
	_ = os.WriteFile(filepath.Join(tempDir, "empty.json"), []byte(""), 0644)
	_ = os.WriteFile(filepath.Join(tempDir, "no-type.json"), []byte(`{"email": "test@example.com"}`), 0644)

	// Create one valid file
	validData, _ := json.Marshal(map[string]any{"type": "claude", "email": "valid@example.com"})
	_ = os.WriteFile(filepath.Join(tempDir, "valid.json"), validData, 0644)

	synth := NewFileSynthesizer()
	ctx := &SynthesisContext{
		Config:      &config.Config{},
		AuthDir:     tempDir,
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("only valid auth file should be processed, got %d", len(auths))
	}
	if auths[0].Label != "valid@example.com" {
		t.Errorf("expected label valid@example.com, got %s", auths[0].Label)
	}
}

func TestFileSynthesizer_Synthesize_SkipsDirectories(t *testing.T) {
	tempDir := t.TempDir()

	// Create a subdirectory with a json file inside
	subDir := filepath.Join(tempDir, "subdir.json")
	err := os.Mkdir(subDir, 0755)
	if err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}

	// Create a valid file in root
	validData, _ := json.Marshal(map[string]any{"type": "claude"})
	_ = os.WriteFile(filepath.Join(tempDir, "valid.json"), validData, 0644)

	synth := NewFileSynthesizer()
	ctx := &SynthesisContext{
		Config:      &config.Config{},
		AuthDir:     tempDir,
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth, got %d", len(auths))
	}
}

func TestFileSynthesizer_Synthesize_RelativeID(t *testing.T) {
	tempDir := t.TempDir()

	authData := map[string]any{"type": "claude"}
	data, _ := json.Marshal(authData)
	err := os.WriteFile(filepath.Join(tempDir, "my-auth.json"), data, 0644)
	if err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}

	synth := NewFileSynthesizer()
	ctx := &SynthesisContext{
		Config:      &config.Config{},
		AuthDir:     tempDir,
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth, got %d", len(auths))
	}

	// ID should be relative path
	if auths[0].ID != "my-auth.json" {
		t.Errorf("expected ID my-auth.json, got %s", auths[0].ID)
	}
}

func TestFileSynthesizer_Synthesize_PrefixValidation(t *testing.T) {
	tests := []struct {
		name       string
		prefix     string
		wantPrefix string
	}{
		{"valid prefix", "myprefix", "myprefix"},
		{"prefix with slashes trimmed", "/myprefix/", "myprefix"},
		{"prefix with spaces trimmed", "  myprefix  ", "myprefix"},
		{"prefix with internal slash rejected", "my/prefix", ""},
		{"empty prefix", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			authData := map[string]any{
				"type":   "claude",
				"prefix": tt.prefix,
			}
			data, _ := json.Marshal(authData)
			_ = os.WriteFile(filepath.Join(tempDir, "auth.json"), data, 0644)

			synth := NewFileSynthesizer()
			ctx := &SynthesisContext{
				Config:      &config.Config{},
				AuthDir:     tempDir,
				Now:         time.Now(),
				IDGenerator: NewStableIDGenerator(),
			}

			auths, err := synth.Synthesize(ctx)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(auths) != 1 {
				t.Fatalf("expected 1 auth, got %d", len(auths))
			}
			if auths[0].Prefix != tt.wantPrefix {
				t.Errorf("expected prefix %q, got %q", tt.wantPrefix, auths[0].Prefix)
			}
		})
	}
}

func TestSynthesizeGeminiVirtualAuths_NilInputs(t *testing.T) {
	now := time.Now()

	if SynthesizeGeminiVirtualAuths(nil, nil, now) != nil {
		t.Error("expected nil for nil primary")
	}
	if SynthesizeGeminiVirtualAuths(&coreauth.Auth{}, nil, now) != nil {
		t.Error("expected nil for nil metadata")
	}
	if SynthesizeGeminiVirtualAuths(nil, map[string]any{}, now) != nil {
		t.Error("expected nil for nil primary with metadata")
	}
}

func TestSynthesizeGeminiVirtualAuths_SingleProject(t *testing.T) {
	now := time.Now()
	primary := &coreauth.Auth{
		ID:       "test-id",
		Provider: "gemini-cli",
		Label:    "test@example.com",
	}
	metadata := map[string]any{
		"project_id": "single-project",
		"email":      "test@example.com",
		"type":       "gemini",
	}

	virtuals := SynthesizeGeminiVirtualAuths(primary, metadata, now)
	if virtuals != nil {
		t.Error("single project should not create virtuals")
	}
}

func TestSynthesizeGeminiVirtualAuths_MultiProject(t *testing.T) {
	now := time.Now()
	primary := &coreauth.Auth{
		ID:       "primary-id",
		Provider: "gemini-cli",
		Label:    "test@example.com",
		Prefix:   "test-prefix",
		ProxyURL: "http://proxy.local",
		Attributes: map[string]string{
			"source": "test-source",
			"path":   "/path/to/auth",
		},
	}
	metadata := map[string]any{
		"project_id": "project-a, project-b, project-c",
		"email":      "test@example.com",
		"type":       "gemini",
	}

	virtuals := SynthesizeGeminiVirtualAuths(primary, metadata, now)

	if len(virtuals) != 3 {
		t.Fatalf("expected 3 virtuals, got %d", len(virtuals))
	}

	// Check primary is disabled
	if !primary.Disabled {
		t.Error("expected primary to be disabled")
	}
	if primary.Status != coreauth.StatusDisabled {
		t.Errorf("expected primary status disabled, got %s", primary.Status)
	}
	if primary.Attributes["gemini_virtual_primary"] != "true" {
		t.Error("expected gemini_virtual_primary=true")
	}
	if !strings.Contains(primary.Attributes["virtual_children"], "project-a") {
		t.Error("expected virtual_children to contain project-a")
	}

	// Check virtuals
	projectIDs := []string{"project-a", "project-b", "project-c"}
	for i, v := range virtuals {
		if v.Provider != "gemini-cli" {
			t.Errorf("expected provider gemini-cli, got %s", v.Provider)
		}
		if v.Status != coreauth.StatusActive {
			t.Errorf("expected status active, got %s", v.Status)
		}
		if v.Prefix != "test-prefix" {
			t.Errorf("expected prefix test-prefix, got %s", v.Prefix)
		}
		if v.ProxyURL != "http://proxy.local" {
			t.Errorf("expected proxy_url http://proxy.local, got %s", v.ProxyURL)
		}
		if v.Attributes["runtime_only"] != "true" {
			t.Error("expected runtime_only=true")
		}
		if v.Attributes["gemini_virtual_parent"] != "primary-id" {
			t.Errorf("expected gemini_virtual_parent=primary-id, got %s", v.Attributes["gemini_virtual_parent"])
		}
		if v.Attributes["gemini_virtual_project"] != projectIDs[i] {
			t.Errorf("expected gemini_virtual_project=%s, got %s", projectIDs[i], v.Attributes["gemini_virtual_project"])
		}
		if !strings.Contains(v.Label, "["+projectIDs[i]+"]") {
			t.Errorf("expected label to contain [%s], got %s", projectIDs[i], v.Label)
		}
	}
}

func TestSynthesizeGeminiVirtualAuths_EmptyProviderAndLabel(t *testing.T) {
	now := time.Now()
	// Test with empty Provider and Label to cover fallback branches
	primary := &coreauth.Auth{
		ID:         "primary-id",
		Provider:   "", // empty provider - should default to gemini-cli
		Label:      "", // empty label - should default to provider
		Attributes: map[string]string{},
	}
	metadata := map[string]any{
		"project_id": "proj-a, proj-b",
		"email":      "user@example.com",
		"type":       "gemini",
	}

	virtuals := SynthesizeGeminiVirtualAuths(primary, metadata, now)

	if len(virtuals) != 2 {
		t.Fatalf("expected 2 virtuals, got %d", len(virtuals))
	}

	// Check that empty provider defaults to gemini-cli
	if virtuals[0].Provider != "gemini-cli" {
		t.Errorf("expected provider gemini-cli (default), got %s", virtuals[0].Provider)
	}
	// Check that empty label defaults to provider
	if !strings.Contains(virtuals[0].Label, "gemini-cli") {
		t.Errorf("expected label to contain gemini-cli, got %s", virtuals[0].Label)
	}
}

func TestSynthesizeGeminiVirtualAuths_NilPrimaryAttributes(t *testing.T) {
	now := time.Now()
	primary := &coreauth.Auth{
		ID:         "primary-id",
		Provider:   "gemini-cli",
		Label:      "test@example.com",
		Attributes: nil, // nil attributes
	}
	metadata := map[string]any{
		"project_id": "proj-a, proj-b",
		"email":      "test@example.com",
		"type":       "gemini",
	}

	virtuals := SynthesizeGeminiVirtualAuths(primary, metadata, now)

	if len(virtuals) != 2 {
		t.Fatalf("expected 2 virtuals, got %d", len(virtuals))
	}
	// Nil attributes should be initialized
	if primary.Attributes == nil {
		t.Error("expected primary.Attributes to be initialized")
	}
	if primary.Attributes["gemini_virtual_primary"] != "true" {
		t.Error("expected gemini_virtual_primary=true")
	}
}

func TestSplitGeminiProjectIDs(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]any
		want     []string
	}{
		{
			name:     "single project",
			metadata: map[string]any{"project_id": "proj-a"},
			want:     []string{"proj-a"},
		},
		{
			name:     "multiple projects",
			metadata: map[string]any{"project_id": "proj-a, proj-b, proj-c"},
			want:     []string{"proj-a", "proj-b", "proj-c"},
		},
		{
			name:     "with duplicates",
			metadata: map[string]any{"project_id": "proj-a, proj-b, proj-a"},
			want:     []string{"proj-a", "proj-b"},
		},
		{
			name:     "with empty parts",
			metadata: map[string]any{"project_id": "proj-a, , proj-b, "},
			want:     []string{"proj-a", "proj-b"},
		},
		{
			name:     "empty project_id",
			metadata: map[string]any{"project_id": ""},
			want:     nil,
		},
		{
			name:     "no project_id",
			metadata: map[string]any{},
			want:     nil,
		},
		{
			name:     "whitespace only",
			metadata: map[string]any{"project_id": "   "},
			want:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitGeminiProjectIDs(tt.metadata)
			if len(got) != len(tt.want) {
				t.Fatalf("expected %v, got %v", tt.want, got)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("expected %v, got %v", tt.want, got)
					break
				}
			}
		})
	}
}

func TestFileSynthesizer_Synthesize_MultiProjectGemini(t *testing.T) {
	tempDir := t.TempDir()

	// Create a gemini auth file with multiple projects
	authData := map[string]any{
		"type":       "gemini",
		"email":      "multi@example.com",
		"project_id": "project-a, project-b, project-c",
	}
	data, _ := json.Marshal(authData)
	err := os.WriteFile(filepath.Join(tempDir, "gemini-multi.json"), data, 0644)
	if err != nil {
		t.Fatalf("failed to write auth file: %v", err)
	}

	synth := NewFileSynthesizer()
	ctx := &SynthesisContext{
		Config:      &config.Config{},
		AuthDir:     tempDir,
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should have 4 auths: 1 primary (disabled) + 3 virtuals
	if len(auths) != 4 {
		t.Fatalf("expected 4 auths (1 primary + 3 virtuals), got %d", len(auths))
	}

	// First auth should be the primary (disabled)
	primary := auths[0]
	if !primary.Disabled {
		t.Error("expected primary to be disabled")
	}
	if primary.Status != coreauth.StatusDisabled {
		t.Errorf("expected primary status disabled, got %s", primary.Status)
	}

	// Remaining auths should be virtuals
	for i := 1; i < 4; i++ {
		v := auths[i]
		if v.Status != coreauth.StatusActive {
			t.Errorf("expected virtual %d to be active, got %s", i, v.Status)
		}
		if v.Attributes["gemini_virtual_parent"] != primary.ID {
			t.Errorf("expected virtual %d parent to be %s, got %s", i, primary.ID, v.Attributes["gemini_virtual_parent"])
		}
	}
}

func TestBuildGeminiVirtualID(t *testing.T) {
	tests := []struct {
		name      string
		baseID    string
		projectID string
		want      string
	}{
		{
			name:      "basic",
			baseID:    "auth.json",
			projectID: "my-project",
			want:      "auth.json::my-project",
		},
		{
			name:      "with slashes",
			baseID:    "path/to/auth.json",
			projectID: "project/with/slashes",
			want:      "path/to/auth.json::project_with_slashes",
		},
		{
			name:      "with spaces",
			baseID:    "auth.json",
			projectID: "my project",
			want:      "auth.json::my_project",
		},
		{
			name:      "empty project",
			baseID:    "auth.json",
			projectID: "",
			want:      "auth.json::project",
		},
		{
			name:      "whitespace project",
			baseID:    "auth.json",
			projectID: "   ",
			want:      "auth.json::project",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildGeminiVirtualID(tt.baseID, tt.projectID)
			if got != tt.want {
				t.Errorf("expected %q, got %q", tt.want, got)
			}
		})
	}
}
