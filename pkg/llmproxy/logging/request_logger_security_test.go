package logging

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateFilename_SanitizesRequestIDForPathSafety(t *testing.T) {
	t.Parallel()

	logsDir := t.TempDir()
	logger := NewFileRequestLogger(true, logsDir, "", 0)

	filename := logger.generateFilename("/v1/responses", "../escape-path")
	resolved := filepath.Join(logsDir, filename)
	rel, err := filepath.Rel(logsDir, resolved)
	if err != nil {
		t.Fatalf("filepath.Rel failed: %v", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("generated filename escaped logs dir: %s", filename)
	}
	if strings.Contains(filename, "/") {
		t.Fatalf("generated filename contains path separator: %s", filename)
	}
}
