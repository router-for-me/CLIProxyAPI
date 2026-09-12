package interactions_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestInteractionsTranslatorsDoNotImportGeminiTranslators(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", "..", ".."))
	scanDirs := []string{
		"internal/translator/openai/interactions",
		"internal/translator/claude/interactions",
		"internal/translator/codex/interactions",
		"internal/translator/antigravity/interactions",
	}
	forbidden := regexp.MustCompile(`"github\.com/router-for-me/CLIProxyAPI/v7/internal/translator/[^"]*/gemini[^"]*"`)
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
