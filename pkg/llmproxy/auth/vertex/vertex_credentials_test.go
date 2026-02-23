package vertex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVertexCredentialStorage_SaveTokenToFile(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "vertex-token.json")

	s := &VertexCredentialStorage{
		ServiceAccount: map[string]any{
			"project_id":   "test-project",
			"client_email": "test@example.com",
		},
		ProjectID: "test-project",
		Email:     "test@example.com",
	}

	err := s.SaveTokenToFile(path)
	if err != nil {
		t.Fatalf("SaveTokenToFile failed: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read file: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("saved file is empty")
	}
}

func TestVertexCredentialStorage_NilChecks(t *testing.T) {
	var s *VertexCredentialStorage
	err := s.SaveTokenToFile("path")
	if err == nil {
		t.Error("expected error for nil storage")
	}

	s = &VertexCredentialStorage{}
	err = s.SaveTokenToFile("path")
	if err == nil {
		t.Error("expected error for empty service account")
	}
}

func TestVertexCredentialStorage_SaveTokenToFileRejectsTraversalPath(t *testing.T) {
	t.Parallel()

	s := &VertexCredentialStorage{
		ServiceAccount: map[string]any{"project_id": "p"},
	}

	err := s.SaveTokenToFile("../vertex.json")
	if err == nil {
		t.Fatal("expected error for traversal path")
	}
	if !strings.Contains(err.Error(), "auth file path is invalid") {
		t.Fatalf("expected invalid path error, got %v", err)
	}
}
