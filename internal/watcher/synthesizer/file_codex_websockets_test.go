package synthesizer

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestSynthesizeAuthFile_CodexWebsocketsDefault(t *testing.T) {
	off := false
	tests := []struct {
		name string
		file string
		cfg  config.CodexConfig
		want string
	}{
		{name: "unset defaults on", file: `{"type":"codex"}`, want: "true"},
		{name: "file opt-out wins", file: `{"type":"codex","websockets":false}`, want: ""},
		{name: "config opt-out", file: `{"type":"codex"}`, cfg: config.CodexConfig{OAuthWebsockets: &off}, want: ""},
		{name: "other providers untouched", file: `{"type":"claude"}`, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			cfg := &config.Config{}
			cfg.Codex = tt.cfg
			auths, err := SynthesizeAuthFile(&SynthesisContext{
				Config:      cfg,
				AuthDir:     dir,
				Now:         time.Now(),
				IDGenerator: NewStableIDGenerator(),
			}, filepath.Join(dir, "auth.json"), []byte(tt.file))
			if err != nil || len(auths) != 1 {
				t.Fatalf("SynthesizeAuthFile() = %d auths, err %v", len(auths), err)
			}
			if got := auths[0].Attributes["websockets"]; got != tt.want {
				t.Fatalf("websockets attribute = %q, want %q", got, tt.want)
			}
		})
	}
}
