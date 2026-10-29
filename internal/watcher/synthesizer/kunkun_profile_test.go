package synthesizer

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/browser"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestSynthesizeAuthFileRestoresKunKunProfileID(t *testing.T) {
	const profileID = "codex-kunkun-0123456789ab"
	ctx := &SynthesisContext{
		Config:      &config.Config{},
		AuthDir:     "/tmp/auths",
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}
	data := []byte(`{"type":"codex","email":"a@example.test","access_token":"a","refresh_token":"r","kunkun_profile_id":"` + profileID + `"}`)

	auths, errSynthesize := SynthesizeAuthFile(ctx, "/tmp/auths/codex-a.json", data)
	if errSynthesize != nil {
		t.Fatalf("SynthesizeAuthFile() error = %v", errSynthesize)
	}
	if len(auths) != 1 {
		t.Fatalf("auths = %d, want 1", len(auths))
	}
	if got := auths[0].Attributes[browser.KunKunProfileIDAttribute]; got != profileID {
		t.Fatalf("%s = %q, want %q", browser.KunKunProfileIDAttribute, got, profileID)
	}
}
