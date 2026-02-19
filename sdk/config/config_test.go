package config

import (
	"os"
	"testing"
)

func TestConfigWrappers(t *testing.T) {
	tmpFile, _ := os.CreateTemp("", "config*.yaml")
	defer os.Remove(tmpFile.Name())
	_, _ = tmpFile.Write([]byte("{}"))
	_ = tmpFile.Close()

	cfg, err := LoadConfig(tmpFile.Name())
	if err != nil {
		t.Errorf("LoadConfig failed: %v", err)
	}
	if cfg == nil {
		t.Fatal("LoadConfig returned nil")
	}

	cfg, err = LoadConfigOptional(tmpFile.Name(), true)
	if err != nil {
		t.Errorf("LoadConfigOptional failed: %v", err)
	}

	err = SaveConfigPreserveComments(tmpFile.Name(), cfg)
	if err != nil {
		t.Errorf("SaveConfigPreserveComments failed: %v", err)
	}

	err = SaveConfigPreserveCommentsUpdateNestedScalar(tmpFile.Name(), []string{"debug"}, "true")
	if err != nil {
		t.Errorf("SaveConfigPreserveCommentsUpdateNestedScalar failed: %v", err)
	}

	data := NormalizeCommentIndentation([]byte("  # comment"))
	if len(data) == 0 {
		t.Error("NormalizeCommentIndentation returned empty")
	}
}
