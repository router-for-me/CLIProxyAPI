package synthesizer

import (
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestSynthesizeAuthFileCodexWebsocketsDefault(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		configDefault bool
		raw           string
		wantAttribute string
	}{
		{name: "config default applies to a file without the field", configDefault: true, raw: `{"type":"codex","email":"a@example.com"}`, wantAttribute: "true"},
		{name: "explicit false in the file wins", configDefault: true, raw: `{"type":"codex","email":"a@example.com","websockets":false}`},
		{name: "explicit true in the file needs no attribute", configDefault: true, raw: `{"type":"codex","email":"a@example.com","websockets":true}`},
		{name: "config default off leaves the file alone", raw: `{"type":"codex","email":"a@example.com"}`},
		{name: "other providers are unaffected", configDefault: true, raw: `{"type":"claude","email":"a@example.com"}`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			fullPath := filepath.Join(t.TempDir(), "credential.json")
			cfg := &config.Config{}
			cfg.Codex.Websockets = testCase.configDefault
			ctx := &SynthesisContext{Config: cfg, AuthDir: filepath.Dir(fullPath)}

			auths, errSynthesize := SynthesizeAuthFile(ctx, fullPath, []byte(testCase.raw))
			if errSynthesize != nil {
				t.Fatalf("SynthesizeAuthFile() error = %v", errSynthesize)
			}
			if len(auths) != 1 {
				t.Fatalf("SynthesizeAuthFile() len = %d, want 1", len(auths))
			}
			if got := auths[0].Attributes["websockets"]; got != testCase.wantAttribute {
				t.Fatalf("websockets attribute = %q, want %q", got, testCase.wantAttribute)
			}
		})
	}
}
