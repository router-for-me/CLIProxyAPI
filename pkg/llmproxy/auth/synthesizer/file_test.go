package synthesizer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/config"
	coreauth "github.com/kooshapari/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/kooshapari/CLIProxyAPI/v7/sdk/pluginapi"
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
		"headers": map[string]string{
			" X-Test ": " value ",
			"X-Empty":  "  ",
		},
		"disable_cooling": true,
		"request_retry":   2,
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
	if got := auths[0].Attributes["header:X-Test"]; got != "value" {
		t.Errorf("expected header:X-Test value, got %q", got)
	}
	if _, ok := auths[0].Attributes["header:X-Empty"]; ok {
		t.Errorf("expected header:X-Empty to be absent, got %q", auths[0].Attributes["header:X-Empty"])
	}
	if v, ok := auths[0].Metadata["disable_cooling"].(bool); !ok || !v {
		t.Errorf("expected disable_cooling true, got %v", auths[0].Metadata["disable_cooling"])
	}
	if v, ok := auths[0].Metadata["request_retry"].(float64); !ok || int(v) != 2 {
		t.Errorf("expected request_retry 2, got %v", auths[0].Metadata["request_retry"])
	}
	if auths[0].Status != coreauth.StatusActive {
		t.Errorf("expected status active, got %s", auths[0].Status)
	}
}

func TestFileSynthesizer_Synthesize_IgnoresGeminiProviderFile(t *testing.T) {
	tempDir := t.TempDir()

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
	if len(auths) != 0 {
		t.Fatalf("expected Gemini auth file to be ignored, got %d auths", len(auths))
	}
}

func TestSynthesizeAuthFileExpandsPluginMultiAuths(t *testing.T) {
	tempDir := t.TempDir()
	fullPath := filepath.Join(tempDir, "geminicli.json")
	raw := []byte(`{"type":"gemini-cli","excluded_models":["model-a"],"headers":{"X-Test":"value"}}`)

	ctx := &SynthesisContext{
		Config:  &config.Config{},
		AuthDir: tempDir,
		Now:     time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC),
		PluginAuthParser: multiAuthParserFunc(func(ctx context.Context, req pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
			if req.Provider != "gemini-cli" || req.Path != fullPath || req.FileName != "geminicli.json" {
				t.Fatalf("ParseAuths request = %#v, want file context", req)
			}
			return []*coreauth.Auth{
				{
					ID:       "geminicli.json",
					Provider: "gemini-cli",
					Metadata: map[string]any{
						"type": "gemini-cli",
						"headers": map[string]any{
							"X-Test": "value",
						},
					},
				},
				nil,
				{
					ID:       "geminicli-project-a.json",
					Provider: "gemini-cli",
					Metadata: map[string]any{
						"type":       "gemini-cli",
						"project_id": "project-a",
						"headers": map[string]any{
							"X-Test": "value",
						},
					},
				},
			}, true, nil
		}),
	}

	auths := SynthesizeAuthFile(ctx, fullPath, raw)
	if len(auths) != 2 {
		t.Fatalf("SynthesizeAuthFile() len = %d, want two plugin auths", len(auths))
	}
	if firstIndex, secondIndex := auths[0].EnsureIndex(), auths[1].EnsureIndex(); firstIndex == "" || firstIndex == secondIndex {
		t.Fatalf("auth indexes = %q/%q, want distinct non-empty indexes", firstIndex, secondIndex)
	}
	for _, auth := range auths {
		if !coreauth.IsPluginVirtualAuth(auth) {
			t.Fatalf("auth attributes = %#v, want plugin virtual marker", auth.Attributes)
		}
		if auth.Attributes[coreauth.AttributeVirtualSource] != fullPath {
			t.Fatalf("virtual_source = %q, want %q", auth.Attributes[coreauth.AttributeVirtualSource], fullPath)
		}
		if auth.Attributes["path"] != fullPath || auth.Attributes["source"] != fullPath {
			t.Fatalf("auth attributes = %#v, want source path", auth.Attributes)
		}
		if gotHeader := auth.Attributes["header:X-Test"]; gotHeader != "value" {
			t.Fatalf("header:X-Test = %q, want value", gotHeader)
		}
		if gotKind := auth.Attributes["auth_kind"]; gotKind != "oauth" {
			t.Fatalf("auth_kind = %q, want oauth", gotKind)
		}
	}
	if gotProject := auths[1].Metadata["project_id"]; gotProject != "project-a" {
		t.Fatalf("project_id = %#v, want project-a", gotProject)
	}
}

func TestSynthesizeAuthFilePluginHandledEmptySuppressesBuiltin(t *testing.T) {
	tempDir := t.TempDir()
	fullPath := filepath.Join(tempDir, "codex.json")
	raw := []byte(`{"type":"codex","access_token":"token"}`)

	ctx := &SynthesisContext{
		Config:  &config.Config{},
		AuthDir: tempDir,
		Now:     time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC),
		PluginAuthParser: multiAuthParserFunc(func(context.Context, pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
			return nil, true, nil
		}),
	}

	auths := SynthesizeAuthFile(ctx, fullPath, raw)
	if len(auths) != 0 {
		t.Fatalf("SynthesizeAuthFile() len = %d, want plugin-handled empty result", len(auths))
	}
}

type multiAuthParserFunc func(context.Context, pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error)

func (f multiAuthParserFunc) ParseAuth(context.Context, pluginapi.AuthParseRequest) (*coreauth.Auth, bool, error) {
	return nil, false, nil
}

func (f multiAuthParserFunc) ParseAuths(ctx context.Context, req pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
	return f(ctx, req)
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

func TestFileSynthesizer_Synthesize_PriorityParsing(t *testing.T) {
	tests := []struct {
		name     string
		priority any
		want     string
		hasValue bool
	}{
		{
			name:     "string with spaces",
			priority: " 10 ",
			want:     "10",
			hasValue: true,
		},
		{
			name:     "number",
			priority: 8,
			want:     "8",
			hasValue: true,
		},
		{
			name:     "invalid string",
			priority: "1x",
			hasValue: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tempDir := t.TempDir()
			authData := map[string]any{
				"type":     "claude",
				"priority": tt.priority,
			}
			data, _ := json.Marshal(authData)
			errWriteFile := os.WriteFile(filepath.Join(tempDir, "auth.json"), data, 0644)
			if errWriteFile != nil {
				t.Fatalf("failed to write auth file: %v", errWriteFile)
			}

			synth := NewFileSynthesizer()
			ctx := &SynthesisContext{
				Config:      &config.Config{},
				AuthDir:     tempDir,
				Now:         time.Now(),
				IDGenerator: NewStableIDGenerator(),
			}

			auths, errSynthesize := synth.Synthesize(ctx)
			if errSynthesize != nil {
				t.Fatalf("unexpected error: %v", errSynthesize)
			}
			if len(auths) != 1 {
				t.Fatalf("expected 1 auth, got %d", len(auths))
			}

			value, ok := auths[0].Attributes["priority"]
			if tt.hasValue {
				if !ok {
					t.Fatal("expected priority attribute to be set")
				}
				if value != tt.want {
					t.Fatalf("expected priority %q, got %q", tt.want, value)
				}
				return
			}
			if ok {
				t.Fatalf("expected priority attribute to be absent, got %q", value)
			}
		})
	}
}

func TestFileSynthesizer_Synthesize_OAuthExcludedModelsMerged(t *testing.T) {
	tempDir := t.TempDir()
	authData := map[string]any{
		"type":            "claude",
		"excluded_models": []string{"custom-model", "MODEL-B"},
	}
	data, _ := json.Marshal(authData)
	errWriteFile := os.WriteFile(filepath.Join(tempDir, "auth.json"), data, 0644)
	if errWriteFile != nil {
		t.Fatalf("failed to write auth file: %v", errWriteFile)
	}

	synth := NewFileSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			OAuthExcludedModels: map[string][]string{
				"claude": {"shared", "model-b"},
			},
		},
		AuthDir:     tempDir,
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, errSynthesize := synth.Synthesize(ctx)
	if errSynthesize != nil {
		t.Fatalf("unexpected error: %v", errSynthesize)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth, got %d", len(auths))
	}

	got := auths[0].Attributes["excluded_models"]
	want := "custom-model,model-b,shared"
	if got != want {
		t.Fatalf("expected excluded_models %q, got %q", want, got)
	}
}

func TestFileSynthesizer_Synthesize_OAuthModelAliases(t *testing.T) {
	tempDir := t.TempDir()
	authData := map[string]any{
		"type":  "codex",
		"email": "codex@example.com",
		"model-aliases": []map[string]any{
			{"name": " gpt-5.3-codex-spark ", "alias": " gpt-5.5 "},
			{"name": "gpt-5.3-codex-spark", "alias": "gpt-5.4", "fork": true},
			{"name": "gpt-5.3-codex-spark", "alias": "gpt-5.5"},
			{"name": "", "alias": "ignored"},
		},
	}
	data, _ := json.Marshal(authData)
	errWriteFile := os.WriteFile(filepath.Join(tempDir, "codex-auth.json"), data, 0644)
	if errWriteFile != nil {
		t.Fatalf("failed to write auth file: %v", errWriteFile)
	}

	synth := NewFileSynthesizer()
	ctx := &SynthesisContext{
		Config:      &config.Config{},
		AuthDir:     tempDir,
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, errSynthesize := synth.Synthesize(ctx)
	if errSynthesize != nil {
		t.Fatalf("unexpected error: %v", errSynthesize)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth, got %d", len(auths))
	}

	got := auths[0].Attributes["model_aliases"]
	want := `[{"name":"gpt-5.3-codex-spark","alias":"gpt-5.5"},{"name":"gpt-5.3-codex-spark","alias":"gpt-5.4","fork":true}]`
	if got != want {
		t.Fatalf("expected model_aliases %q, got %q", want, got)
	}
}

func TestFileSynthesizer_Synthesize_IgnoresGeminiOAuthFile(t *testing.T) {
	tempDir := t.TempDir()

	authData := map[string]any{
		"type":       "gemini",
		"email":      "multi@example.com",
		"project_id": "project-a, project-b, project-c",
		"priority":   " 10 ",
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
	if gotPriority := primary.Attributes["priority"]; gotPriority != "10" {
		t.Errorf("expected primary priority 10, got %q", gotPriority)
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
		if gotPriority := v.Attributes["priority"]; gotPriority != "10" {
			t.Errorf("expected virtual %d priority 10, got %q", i, gotPriority)
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
