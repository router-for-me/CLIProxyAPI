package interactions_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var forbidden = regexp.MustCompile(`"github\.com/router-for-me/CLIProxyAPI/v[0-9]+/internal/translator/[^"]*/gemini[^"]*"`)

func TestInteractionsTranslatorsDoNotImportGeminiTranslators(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	scanDirs := []string{
		"internal/translator/openai/interactions",
		"internal/translator/claude/interactions",
		"internal/translator/codex/interactions",
		"internal/translator/antigravity/interactions",
	}
	var violations []string
	for _, scanDir := range scanDirs {
		root := filepath.Join(repoRoot, scanDir)
		errWalk := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			data, errRead := os.ReadFile(path)
			if errRead != nil {
				return errRead
			}
			if forbidden.Match(data) {
				rel, errRel := filepath.Rel(repoRoot, path)
				if errRel != nil {
					rel = path
				}
				violations = append(violations, rel)
			}
			return nil
		})
		if errWalk != nil {
			t.Fatalf("scan %s: %v", scanDir, errWalk)
		}
	}
	if len(violations) > 0 {
		t.Fatalf("non-Gemini Interactions translators import Gemini translators: %s", strings.Join(violations, ", "))
	}
}

func TestInteractionsGeminiImportBoundaryMatchesModuleVersions(t *testing.T) {
	for _, tc := range []struct {
		path string
		want bool
	}{
		{`"github.com/router-for-me/CLIProxyAPI/v7/internal/translator/claude/gemini"`, true},
		{`"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/gemini"`, true},
		{`"github.com/router-for-me/CLIProxyAPI/v9/internal/translator/codex/gemini"`, true},
		{`"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"`, false},
		{`"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/openai/interactions"`, false},
		{`"githubXcom/router-for-me/CLIProxyAPI/v8/internal/translator/openai/gemini"`, false},
	} {
		if got := forbidden.MatchString(tc.path); got != tc.want {
			t.Errorf("import boundary for %s = %v, want %v", tc.path, got, tc.want)
		}
	}
}
