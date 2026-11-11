package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestGitPublicationReadsLegacyJournal(t *testing.T) {
	repoDir := t.TempDir()
	authDir := filepath.Join(repoDir, "auths")
	relPath := "auths/team/account.json"
	marker := gitPublicationPath(repoDir, relPath)
	if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"path":"auths/team/account.json","sha256":"` + strings.Repeat("a", 64) + `","staging":"/staging/credential","base_commit":"` + strings.Repeat("b", 40) + `"}`
	if err := os.WriteFile(marker, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readGitPublication(marker, repoDir, authDir)
	if err != nil || got.Path != relPath || got.Staging != "/staging/credential" || got.SHA256 != strings.Repeat("a", 64) || got.Base != strings.Repeat("b", 40) {
		t.Fatalf("legacy journal read = %+v, %v", got, err)
	}
	for _, field := range []string{"path", "sha256", "staging", "base_commit"} {
		t.Run(field, func(t *testing.T) {
			doc := ojson.NewDocString(legacy)
			if err := doc.Set(field, true); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(marker, doc.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readGitPublication(marker, repoDir, authDir); err == nil {
				t.Fatal("accepted a malformed journal field")
			}
		})
	}
}
