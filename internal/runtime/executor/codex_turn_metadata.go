package executor

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	git "github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/google/uuid"
)

const codexDefaultSandboxTag = "none"

const codexWorkspaceMetadataCacheTTL = 5 * time.Second

type codexTurnMetadata struct {
	TurnID     string                                `json:"turn_id,omitempty"`
	Workspaces map[string]codexTurnMetadataWorkspace `json:"workspaces,omitempty"`
	Sandbox    string                                `json:"sandbox,omitempty"`
}

type codexTurnMetadataWorkspace struct {
	AssociatedRemoteURLs map[string]string `json:"associated_remote_urls,omitempty"`
	LatestGitCommitHash  string            `json:"latest_git_commit_hash,omitempty"`
	HasChanges           *bool             `json:"has_changes,omitempty"`
}

type codexWorkspaceMetadataCacheEntry struct {
	workspaces map[string]codexTurnMetadataWorkspace
	expireAt   time.Time
}

var codexWorkspaceMetadataCache sync.Map

func codexEnsureTurnMetadataHeader(target http.Header, source http.Header) {
	if target == nil {
		return
	}
	if value := firstNonEmptyHeaderValue(target, source, "X-Codex-Turn-Metadata"); value != "" {
		target.Set("X-Codex-Turn-Metadata", value)
		return
	}
	target.Set("X-Codex-Turn-Metadata", codexDefaultTurnMetadataHeader())
}

func codexDefaultTurnMetadataHeader() string {
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}
	return codexBuildTurnMetadataHeader(uuid.NewString(), codexDefaultSandboxTag, cwd)
}

func codexBuildTurnMetadataHeader(turnID string, sandbox string, cwd string) string {
	payload, err := json.Marshal(codexTurnMetadata{
		TurnID:     strings.TrimSpace(turnID),
		Workspaces: codexWorkspaceMetadata(cwd),
		Sandbox:    strings.TrimSpace(sandbox),
	})
	if err != nil {
		return `{"sandbox":"none"}`
	}
	return string(payload)
}

func codexWorkspaceMetadata(cwd string) map[string]codexTurnMetadataWorkspace {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return nil
	}
	if cached, ok := codexWorkspaceMetadataCache.Load(cwd); ok {
		if entry, okEntry := cached.(codexWorkspaceMetadataCacheEntry); okEntry && time.Now().Before(entry.expireAt) {
			return entry.workspaces
		}
	}

	workspaces := codexLoadWorkspaceMetadata(cwd)
	codexWorkspaceMetadataCache.Store(cwd, codexWorkspaceMetadataCacheEntry{
		workspaces: workspaces,
		expireAt:   time.Now().Add(codexWorkspaceMetadataCacheTTL),
	})
	return workspaces
}

func codexLoadWorkspaceMetadata(cwd string) map[string]codexTurnMetadataWorkspace {
	repoRoot := codexGitRepoRoot(cwd)
	if repoRoot == "" {
		return nil
	}

	repo, err := git.PlainOpenWithOptions(cwd, &git.PlainOpenOptions{
		DetectDotGit:          true,
		EnableDotGitCommonDir: true,
	})
	if err != nil {
		return nil
	}

	workspace := codexTurnMetadataWorkspace{
		AssociatedRemoteURLs: codexGitRemoteURLs(repo),
		LatestGitCommitHash:  codexGitHeadCommitHash(repo),
		HasChanges:           codexGitHasChanges(repo),
	}
	if len(workspace.AssociatedRemoteURLs) == 0 {
		workspace.AssociatedRemoteURLs = nil
	}
	if workspace.LatestGitCommitHash == "" && workspace.HasChanges == nil && len(workspace.AssociatedRemoteURLs) == 0 {
		return nil
	}
	return map[string]codexTurnMetadataWorkspace{
		repoRoot: workspace,
	}
}

func codexGitRepoRoot(cwd string) string {
	if cwd == "" {
		return ""
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		abs = cwd
	}
	current := abs
	for {
		gitPath := filepath.Join(current, ".git")
		if _, errStat := os.Stat(gitPath); errStat == nil {
			return current
		}
		parent := filepath.Dir(current)
		if parent == current {
			return ""
		}
		current = parent
	}
}

func codexGitRemoteURLs(repo *git.Repository) map[string]string {
	if repo == nil {
		return nil
	}
	remotes, err := repo.Remotes()
	if err != nil || len(remotes) == 0 {
		return nil
	}
	urls := make(map[string]string, len(remotes))
	for _, remote := range remotes {
		if remote == nil {
			continue
		}
		cfg := remote.Config()
		if cfg == nil || strings.TrimSpace(cfg.Name) == "" || len(cfg.URLs) == 0 {
			continue
		}
		url := strings.TrimSpace(cfg.URLs[0])
		if url == "" {
			continue
		}
		urls[cfg.Name] = url
	}
	if len(urls) == 0 {
		return nil
	}
	return urls
}

func codexGitHeadCommitHash(repo *git.Repository) string {
	if repo == nil {
		return ""
	}
	head, err := repo.Head()
	if err != nil || head == nil {
		return ""
	}
	if head.Type() != plumbing.HashReference {
		return ""
	}
	return strings.TrimSpace(head.Hash().String())
}

func codexGitHasChanges(repo *git.Repository) *bool {
	if repo == nil {
		return nil
	}
	worktree, err := repo.Worktree()
	if err != nil || worktree == nil {
		return nil
	}
	status, err := worktree.Status()
	if err != nil {
		return nil
	}
	hasChanges := !status.IsClean()
	return &hasChanges
}
