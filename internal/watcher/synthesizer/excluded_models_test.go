package synthesizer

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func mustSynthesizeAuthFile(t *testing.T, ctx *SynthesisContext, fullPath string, raw string) []*coreauth.Auth {
	t.Helper()
	auths, errSynthesize := SynthesizeAuthFile(ctx, fullPath, []byte(raw))
	if errSynthesize != nil {
		t.Fatalf("SynthesizeAuthFile(%s) error = %v", fullPath, errSynthesize)
	}
	return auths
}

func TestExtractExcludedModelsFromMetadata(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata map[string]any
		want     []string
	}{
		{name: "absent", metadata: map[string]any{}},
		{name: "null", metadata: map[string]any{"excluded_models": nil}},
		{name: "empty array", metadata: map[string]any{"excluded_models": []any{}}, want: []string{}},
		{name: "array", metadata: map[string]any{"excluded_models": []any{" a ", "", "b"}}, want: []string{"a", "b"}},
		{name: "string slice", metadata: map[string]any{"excluded_models": []string{"a"}}, want: []string{"a"}},
		{name: "single string", metadata: map[string]any{"excluded_models": "gemini-pro-agent"}, want: []string{"gemini-pro-agent"}},
		{name: "comma string", metadata: map[string]any{"excluded_models": " gemini-pro-agent , ,gemini-3-flash"}, want: []string{"gemini-pro-agent", "gemini-3-flash"}},
		{name: "blank string", metadata: map[string]any{"excluded_models": "  "}, want: []string{}},
		{name: "legacy key string", metadata: map[string]any{"excluded-models": "a,b"}, want: []string{"a", "b"}},
		{name: "canonical key wins", metadata: map[string]any{"excluded_models": []any{}, "excluded-models": []any{"legacy"}}, want: []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, errExtract := ExtractExcludedModelsFromMetadata(tc.metadata)
			if errExtract != nil {
				t.Fatalf("ExtractExcludedModelsFromMetadata() error = %v", errExtract)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ExtractExcludedModelsFromMetadata() = %#v, want %#v", got, tc.want)
			}
		})
	}
	for _, value := range []any{123, true, map[string]any{}, []any{"a", 123}, []any{nil}} {
		if got, errExtract := ExtractExcludedModelsFromMetadata(map[string]any{"excluded_models": value}); errExtract == nil {
			t.Fatalf("ExtractExcludedModelsFromMetadata(%#v) = %v, want error", value, got)
		}
	}
}

func TestSynthesizeAuthFileRejectsInvalidExcludedModels(t *testing.T) {
	authDir := t.TempDir()
	ctx := &SynthesisContext{Config: &config.Config{}, AuthDir: authDir, Now: time.Date(2026, time.October, 10, 12, 0, 0, 0, time.UTC)}
	badPath := filepath.Join(authDir, "bad.json")
	badPayload := `{"type":"claude","email":"bad@example.test","excluded_models":123}`
	auths, errSynthesize := SynthesizeAuthFile(ctx, badPath, []byte(badPayload))
	if errSynthesize == nil || !strings.Contains(errSynthesize.Error(), badPath) || len(auths) != 0 {
		t.Fatalf("SynthesizeAuthFile(bad) = (%d auths, %v), want error naming %s", len(auths), errSynthesize, badPath)
	}

	// Payloads that synthesize no builtin auth must still be rejected so callers never persist them.
	for _, raw := range []string{
		`{"type":"gemini-cli","excluded_models":123}`,
		`{"excluded_models":["a",1]}`,
	} {
		if auths, errSynthesize = SynthesizeAuthFile(ctx, badPath, []byte(raw)); errSynthesize == nil || len(auths) != 0 {
			t.Fatalf("SynthesizeAuthFile(%s) = (%d auths, %v), want error", raw, len(auths), errSynthesize)
		}
	}

	pluginCtx := *ctx
	pluginCtx.PluginAuthParser = multiAuthParserFunc(func(context.Context, pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
		return []*coreauth.Auth{{ID: "plugin.json", Provider: "acme"}}, true, nil
	})
	pluginPath := filepath.Join(authDir, "plugin.json")
	auths, errSynthesize = SynthesizeAuthFile(&pluginCtx, pluginPath, []byte(`{"type":"acme","excluded_models":["a",false]}`))
	if errSynthesize == nil || !strings.Contains(errSynthesize.Error(), pluginPath) || len(auths) != 0 {
		t.Fatalf("SynthesizeAuthFile(plugin bad) = (%d auths, %v), want error naming %s", len(auths), errSynthesize, pluginPath)
	}

	// A directory scan skips the invalid file instead of applying a partial exclusion list.
	if errWrite := os.WriteFile(badPath, []byte(badPayload), 0o600); errWrite != nil {
		t.Fatalf("write bad auth file: %v", errWrite)
	}
	if errWrite := os.WriteFile(filepath.Join(authDir, "good.json"), []byte(`{"type":"claude","email":"good@example.test"}`), 0o600); errWrite != nil {
		t.Fatalf("write good auth file: %v", errWrite)
	}
	scanned, errScan := NewFileSynthesizer().Synthesize(ctx)
	if errScan != nil || len(scanned) != 1 || scanned[0].FileName != "good.json" {
		t.Fatalf("FileSynthesizer.Synthesize() = (%v, %v), want only good.json", scanned, errScan)
	}

	auths = mustSynthesizeAuthFile(t, ctx, filepath.Join(authDir, "legacy.json"), `{"type":"claude","email":"legacy@example.test","excluded_models":"Model-A,model-b"}`)
	if len(auths) != 1 || auths[0].Attributes["excluded_models"] != "model-a,model-b" {
		t.Fatalf("legacy string auths = %+v, want comma string split into two rules", auths)
	}
}

func TestSynthesizeAuthFileDoesNotPersistGlobalOAuthExclusions(t *testing.T) {
	authDir := t.TempDir()
	ctx := &SynthesisContext{
		Config:  &config.Config{OAuthExcludedModels: map[string][]string{"claude": {"global-model"}}},
		AuthDir: authDir,
		Now:     time.Date(2026, time.October, 10, 12, 0, 0, 0, time.UTC),
	}

	for _, raw := range []string{
		`{"type":"claude","email":"inherit@example.test"}`,
		`{"type":"claude","email":"inherit@example.test","excluded_models":[]}`,
		`{"type":"claude","email":"inherit@example.test","excluded_models":[" ","\t"]}`,
	} {
		inherited := mustSynthesizeAuthFile(t, ctx, filepath.Join(authDir, "inherit.json"), raw)
		if len(inherited) != 1 {
			t.Fatalf("%s: len(inherited) = %d, want 1", raw, len(inherited))
		}
		if value, ok := inherited[0].Attributes["excluded_models"]; ok {
			t.Fatalf("%s: inherited excluded_models = %q, want absent so global config is resolved at runtime", raw, value)
		}
		if value, ok := inherited[0].Attributes["excluded_models_hash"]; ok {
			t.Fatalf("%s: inherited excluded_models_hash = %q, want absent", raw, value)
		}
	}

	own := mustSynthesizeAuthFile(t, ctx, filepath.Join(authDir, "own.json"), `{"type":"claude","email":"own@example.test","excluded_models":["Own-Model"]}`)
	if len(own) != 1 {
		t.Fatalf("len(own) = %d, want 1", len(own))
	}
	if got, want := own[0].Attributes["excluded_models"], "own-model"; got != want {
		t.Fatalf("own excluded_models = %q, want %q", got, want)
	}
}
