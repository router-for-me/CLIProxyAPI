package synthesizer

import (
	"encoding/base64"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// A replaced codex auth file carries its plan in the id_token JWT; the
// synthesizer must extract chatgpt_plan_type so the model catalog registers
// the right plan tier (team/pro/free) after a credential swap (#5736).
func TestSynthesizeAuthFileCodexExtractsPlanType(t *testing.T) {
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	header := b64(`{"alg":"none"}`)
	payload := b64(`{"email":"t@example.com","exp":9999999999,"https://api.openai.com/auth":{"chatgpt_plan_type":"team","chatgpt_account_id":"acc-1"}}`)
	jwt := header + "." + payload + ".sig"

	tempDir := t.TempDir()
	fullPath := filepath.Join(tempDir, "codex-team.json")
	raw := []byte(`{"type":"codex","access_token":"at","id_token":"` + jwt + `"}`)

	auths, errSynthesize := SynthesizeAuthFile(&SynthesisContext{
		Config:  &config.Config{},
		AuthDir: tempDir,
		Now:     time.Now(),
	}, fullPath, raw)
	if errSynthesize != nil {
		t.Fatalf("SynthesizeAuthFile() error = %v", errSynthesize)
	}
	if len(auths) != 1 {
		t.Fatalf("auths = %d, want 1", len(auths))
	}
	if got := auths[0].Attributes["plan_type"]; got != "team" {
		t.Fatalf("plan_type = %q, want team", got)
	}
}
