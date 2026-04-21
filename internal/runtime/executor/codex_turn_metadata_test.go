package executor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	git "github.com/go-git/go-git/v6"
	gitconfig "github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing/object"
)

func TestCodexBuildTurnMetadataHeaderKeepsBaseFieldsWithoutGitRepo(t *testing.T) {
	header := codexBuildTurnMetadataHeader("turn-1", codexDefaultSandboxTag, t.TempDir())

	var parsed map[string]any
	if err := json.Unmarshal([]byte(header), &parsed); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got, _ := parsed["turn_id"].(string); got != "turn-1" {
		t.Fatalf("turn_id = %q, want %q", got, "turn-1")
	}
	if got, _ := parsed["sandbox"].(string); got != codexDefaultSandboxTag {
		t.Fatalf("sandbox = %q, want %q", got, codexDefaultSandboxTag)
	}
	if got := parsed["workspaces"]; got != nil {
		t.Fatalf("workspaces = %#v, want nil", got)
	}
}

func TestCodexBuildTurnMetadataHeaderIncludesGitWorkspaceMetadata(t *testing.T) {
	resetCodexWorkspaceMetadataCache()

	repoDir := t.TempDir()
	repo, err := git.PlainInit(repoDir, false)
	if err != nil {
		t.Fatalf("PlainInit() error = %v", err)
	}
	if _, err := repo.CreateRemote(&gitconfig.RemoteConfig{
		Name: "origin",
		URLs: []string{"git@github.com:openai/codex.git"},
	}); err != nil {
		t.Fatalf("CreateRemote() error = %v", err)
	}

	readmePath := filepath.Join(repoDir, "README.md")
	if err := os.WriteFile(readmePath, []byte("hello\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatalf("Worktree() error = %v", err)
	}
	if _, err := worktree.Add("README.md"); err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	if _, err := worktree.Commit("initial", &git.CommitOptions{
		Author: &object.Signature{
			Name:  "CLIProxyAPI",
			Email: "cliproxy@local",
			When:  time.Unix(1, 0),
		},
	}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if err := os.WriteFile(readmePath, []byte("hello world\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() dirty error = %v", err)
	}

	header := codexBuildTurnMetadataHeader("turn-1", codexDefaultSandboxTag, repoDir)

	var parsed struct {
		TurnID     string                                `json:"turn_id"`
		Sandbox    string                                `json:"sandbox"`
		Workspaces map[string]codexTurnMetadataWorkspace `json:"workspaces"`
	}
	if err := json.Unmarshal([]byte(header), &parsed); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if parsed.TurnID != "turn-1" {
		t.Fatalf("turn_id = %q, want %q", parsed.TurnID, "turn-1")
	}
	if parsed.Sandbox != codexDefaultSandboxTag {
		t.Fatalf("sandbox = %q, want %q", parsed.Sandbox, codexDefaultSandboxTag)
	}

	workspace, ok := parsed.Workspaces[repoDir]
	if !ok {
		t.Fatalf("workspaces[%q] missing in %#v", repoDir, parsed.Workspaces)
	}
	if workspace.LatestGitCommitHash == "" {
		t.Fatal("LatestGitCommitHash should be populated")
	}
	if workspace.AssociatedRemoteURLs["origin"] != "git@github.com:openai/codex.git" {
		t.Fatalf("AssociatedRemoteURLs[origin] = %q, want %q", workspace.AssociatedRemoteURLs["origin"], "git@github.com:openai/codex.git")
	}
	if workspace.HasChanges == nil || !*workspace.HasChanges {
		t.Fatalf("HasChanges = %#v, want true", workspace.HasChanges)
	}
}

func resetCodexWorkspaceMetadataCache() {
	codexWorkspaceMetadataCache = sync.Map{}
}
