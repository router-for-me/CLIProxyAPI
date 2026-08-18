package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/client"
	gitindex "github.com/go-git/go-git/v6/plumbing/format/index"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/go-git/go-git/v6/plumbing/transport/http"
	"github.com/go-git/go-git/v6/storage/filesystem/dotgit"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

const (
	// gcInterval defines minimum time between garbage collection runs.
	gcInterval = 5 * time.Minute
	// gcPruneGracePeriod keeps recently orphaned objects available for recovery.
	gcPruneGracePeriod = 24 * time.Hour
)

// GitTokenStore persists token records and auth metadata using git as the backing storage.
type GitTokenStore struct {
	mu        sync.Mutex
	dirLock   sync.RWMutex
	baseDir   string
	repoDir   string
	configDir string
	remote    string
	branch    string
	username  string
	password  string
	lastGC    time.Time
}

type resolvedRemoteBranch struct {
	name plumbing.ReferenceName
	hash plumbing.Hash
}

// NewGitTokenStore creates a token store that saves credentials to disk through the
// TokenStorage implementation embedded in the token record.
// When branch is non-empty, clone/pull/push operations target that branch instead of the remote default.
func NewGitTokenStore(remote, username, password, branch string) *GitTokenStore {
	return &GitTokenStore{
		remote:   remote,
		branch:   strings.TrimSpace(branch),
		username: username,
		password: password,
	}
}

// SetBaseDir updates the default directory used for auth JSON persistence when no explicit path is provided.
func (s *GitTokenStore) SetBaseDir(dir string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	clean := strings.TrimSpace(dir)
	if clean == "" {
		s.dirLock.Lock()
		s.baseDir = ""
		s.repoDir = ""
		s.configDir = ""
		s.dirLock.Unlock()
		return
	}
	if abs, err := filepath.Abs(clean); err == nil {
		clean = abs
	}
	repoDir := filepath.Dir(clean)
	if repoDir == "" || repoDir == "." {
		repoDir = clean
	}
	configDir := filepath.Join(repoDir, "config")
	s.dirLock.Lock()
	s.baseDir = clean
	s.repoDir = repoDir
	s.configDir = configDir
	s.dirLock.Unlock()
}

// AuthDir returns the directory used for auth persistence.
func (s *GitTokenStore) AuthDir() string {
	return s.baseDirSnapshot()
}

// ConfigPath returns the managed config file path.
func (s *GitTokenStore) ConfigPath() string {
	s.dirLock.RLock()
	defer s.dirLock.RUnlock()
	if s.configDir == "" {
		return ""
	}
	return filepath.Join(s.configDir, "config.yaml")
}

// EnsureRepository prepares the local git working tree by cloning or opening the repository.
func (s *GitTokenStore) EnsureRepository() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensureRepositoryLocked()
}

func (s *GitTokenStore) ensureRepositoryLocked() error {
	s.dirLock.Lock()
	if s.remote == "" {
		s.dirLock.Unlock()
		return fmt.Errorf("git token store: remote not configured")
	}
	if s.baseDir == "" {
		s.dirLock.Unlock()
		return fmt.Errorf("git token store: base directory not configured")
	}
	repoDir := s.repoDir
	if repoDir == "" {
		repoDir = filepath.Dir(s.baseDir)
		if repoDir == "" || repoDir == "." {
			repoDir = s.baseDir
		}
		s.repoDir = repoDir
	}
	if s.configDir == "" {
		s.configDir = filepath.Join(repoDir, "config")
	}
	authDir := filepath.Join(repoDir, "auths")
	configDir := filepath.Join(repoDir, "config")
	gitDir := filepath.Join(repoDir, ".git")
	authMethod := s.gitClientOptions()
	var initPaths []string
	if _, err := os.Stat(gitDir); errors.Is(err, fs.ErrNotExist) {
		if errMk := os.MkdirAll(repoDir, 0o700); errMk != nil {
			s.dirLock.Unlock()
			return fmt.Errorf("git token store: create repo dir: %w", errMk)
		}
		cloneOpts := &git.CloneOptions{ClientOptions: authMethod, URL: s.remote}
		if s.branch != "" {
			cloneOpts.ReferenceName = plumbing.NewBranchReferenceName(s.branch)
		}
		if _, errClone := git.PlainClone(repoDir, cloneOpts); errClone != nil {
			if errors.Is(errClone, transport.ErrEmptyRemoteRepository) {
				_ = os.RemoveAll(gitDir)
				repo, errInit := git.PlainInit(repoDir, false)
				if errInit != nil {
					s.dirLock.Unlock()
					return fmt.Errorf("git token store: init empty repo: %w", errInit)
				}
				if s.branch != "" {
					headRef := plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName(s.branch))
					if errHead := repo.Storer.SetReference(headRef); errHead != nil {
						s.dirLock.Unlock()
						return fmt.Errorf("git token store: set head to branch %s: %w", s.branch, errHead)
					}
				}
				if _, errRemote := repo.Remote("origin"); errRemote != nil {
					if _, errCreate := repo.CreateRemote(&config.RemoteConfig{
						Name: "origin",
						URLs: []string{s.remote},
					}); errCreate != nil && !errors.Is(errCreate, git.ErrRemoteExists) {
						s.dirLock.Unlock()
						return fmt.Errorf("git token store: configure remote: %w", errCreate)
					}
				}
				if err := os.MkdirAll(authDir, 0o700); err != nil {
					s.dirLock.Unlock()
					return fmt.Errorf("git token store: create auth dir: %w", err)
				}
				if err := os.MkdirAll(configDir, 0o700); err != nil {
					s.dirLock.Unlock()
					return fmt.Errorf("git token store: create config dir: %w", err)
				}
				if err := ensureEmptyFile(filepath.Join(authDir, ".gitkeep")); err != nil {
					s.dirLock.Unlock()
					return fmt.Errorf("git token store: create auth placeholder: %w", err)
				}
				if err := ensureEmptyFile(filepath.Join(configDir, ".gitkeep")); err != nil {
					s.dirLock.Unlock()
					return fmt.Errorf("git token store: create config placeholder: %w", err)
				}
				initPaths = []string{
					filepath.Join("auths", ".gitkeep"),
					filepath.Join("config", ".gitkeep"),
				}
			} else {
				s.dirLock.Unlock()
				return fmt.Errorf("git token store: clone remote: %w", errClone)
			}
		}
	} else if err != nil {
		s.dirLock.Unlock()
		return fmt.Errorf("git token store: stat repo: %w", err)
	} else {
		repo, errOpen := git.PlainOpen(repoDir)
		if errOpen != nil {
			s.dirLock.Unlock()
			return fmt.Errorf("git token store: open repo: %w", errOpen)
		}
		worktree, errWorktree := repo.Worktree()
		if errWorktree != nil {
			s.dirLock.Unlock()
			return fmt.Errorf("git token store: worktree: %w", errWorktree)
		}
		if errVerify := verifyRepositoryHead(repo); errVerify != nil {
			if !isRepositoryCorruptionError(errVerify) {
				s.dirLock.Unlock()
				return fmt.Errorf("git token store: verify repository before pull: %w", errVerify)
			}
			if errRecover := s.recoverRepositoryLocked(repoDir, authMethod, nil, nil); errRecover != nil {
				s.dirLock.Unlock()
				return fmt.Errorf("git token store: verify repository before pull: %w; recovery failed: %v", errVerify, errRecover)
			}
			repo, errOpen = git.PlainOpen(repoDir)
			if errOpen != nil {
				s.dirLock.Unlock()
				return fmt.Errorf("git token store: open recovered repo: %w", errOpen)
			}
			worktree, errWorktree = repo.Worktree()
			if errWorktree != nil {
				s.dirLock.Unlock()
				return fmt.Errorf("git token store: recovered worktree: %w", errWorktree)
			}
		}
		if s.branch != "" {
			if errCheckout := s.checkoutConfiguredBranch(repo, worktree, authMethod); errCheckout != nil {
				s.dirLock.Unlock()
				return errCheckout
			}
		} else {
			// When branch is unset, ensure the working tree follows the remote default branch
			if err := checkoutRemoteDefaultBranch(repo, worktree, authMethod); err != nil {
				if !shouldFallbackToCurrentBranch(repo, err) {
					s.dirLock.Unlock()
					return fmt.Errorf("git token store: checkout remote default: %w", err)
				}
			}
		}
		pullOpts := &git.PullOptions{ClientOptions: authMethod, RemoteName: "origin"}
		if s.branch != "" {
			pullOpts.ReferenceName = plumbing.NewBranchReferenceName(s.branch)
		}
		prePullHead, errPrePullHead := repo.Head()
		if errPrePullHead != nil && !errors.Is(errPrePullHead, plumbing.ErrReferenceNotFound) {
			s.dirLock.Unlock()
			return fmt.Errorf("git token store: get head before pull: %w", errPrePullHead)
		}
		var prePullTree *object.Tree
		if prePullHead != nil {
			prePullCommit, errPrePullCommit := repo.CommitObject(prePullHead.Hash())
			if errPrePullCommit != nil {
				s.dirLock.Unlock()
				return fmt.Errorf("git token store: inspect head before pull: %w", errPrePullCommit)
			}
			prePullTree, errPrePullCommit = prePullCommit.Tree()
			if errPrePullCommit != nil {
				s.dirLock.Unlock()
				return fmt.Errorf("git token store: inspect tree before pull: %w", errPrePullCommit)
			}
		}
		dirtyPaths, errDirtyPaths := worktreeDirtyPaths(worktree)
		if errDirtyPaths != nil {
			s.dirLock.Unlock()
			return fmt.Errorf("git token store: inspect worktree before pull: %w", errDirtyPaths)
		}
		repositoryRecovered := false
		if errPull := worktree.Pull(pullOpts); errPull != nil {
			switch {
			case errors.Is(errPull, git.NoErrAlreadyUpToDate):
				if errReset := resetIndexToHead(repo, worktree); errReset != nil {
					if !isRepositoryCorruptionError(errReset) {
						s.dirLock.Unlock()
						return fmt.Errorf("git token store: repair index after up-to-date pull: %w", errReset)
					}
					if errRecover := s.recoverRepositoryLocked(repoDir, authMethod, prePullTree, dirtyPaths); errRecover != nil {
						s.dirLock.Unlock()
						return fmt.Errorf("git token store: repair index after up-to-date pull: %w; recovery failed: %v", errReset, errRecover)
					}
					repositoryRecovered = true
				}
			case errors.Is(errPull, git.ErrUnstagedChanges), errors.Is(errPull, git.ErrNonFastForwardUpdate):
				if prePullHead == nil {
					s.dirLock.Unlock()
					return fmt.Errorf("git token store: reconcile pull without a local branch")
				}
				if errReconcile := reconcileRemoteWorktree(repo, worktree, repoDir, prePullHead, dirtyPaths); errReconcile != nil {
					if !isRepositoryCorruptionError(errReconcile) {
						s.dirLock.Unlock()
						return fmt.Errorf("git token store: reconcile remote changes: %w", errReconcile)
					}
					if errRecover := s.recoverRepositoryLocked(repoDir, authMethod, prePullTree, dirtyPaths); errRecover != nil {
						s.dirLock.Unlock()
						return fmt.Errorf("git token store: reconcile remote changes: %w; recovery failed: %v", errReconcile, errRecover)
					}
					repositoryRecovered = true
				}
			case errors.Is(errPull, transport.ErrAuthenticationRequired),
				errors.Is(errPull, transport.ErrEmptyRemoteRepository):
				// Ignore authentication prompts and empty remote references on initial sync.
			case errors.Is(errPull, plumbing.ErrReferenceNotFound):
				if s.branch != "" {
					s.dirLock.Unlock()
					return fmt.Errorf("git token store: pull: %w", errPull)
				}
				// Ignore missing references only when following the remote default branch.
			case isRepositoryCorruptionError(errPull):
				if errRecover := s.recoverRepositoryLocked(repoDir, authMethod, prePullTree, dirtyPaths); errRecover != nil {
					s.dirLock.Unlock()
					return fmt.Errorf("git token store: pull: %w; recovery failed: %v", errPull, errRecover)
				}
				repositoryRecovered = true
			default:
				s.dirLock.Unlock()
				return fmt.Errorf("git token store: pull: %w", errPull)
			}
		}
		if !repositoryRecovered {
			if errVerify := verifyRepositoryHead(repo); errVerify != nil {
				if !isRepositoryCorruptionError(errVerify) {
					s.dirLock.Unlock()
					return fmt.Errorf("git token store: verify repository after pull: %w", errVerify)
				}
				if errRecover := s.recoverRepositoryLocked(repoDir, authMethod, prePullTree, dirtyPaths); errRecover != nil {
					s.dirLock.Unlock()
					return fmt.Errorf("git token store: verify repository after pull: %w; recovery failed: %v", errVerify, errRecover)
				}
				repositoryRecovered = true
			}
		}
		if !repositoryRecovered {
			if errRestore := restoreMissingTrackedFiles(repo, repoDir); errRestore != nil {
				s.dirLock.Unlock()
				return fmt.Errorf("git token store: restore tracked worktree files: %w", errRestore)
			}
		}
	}
	if err := disableGitCommitSigning(repoDir); err != nil {
		s.dirLock.Unlock()
		return err
	}
	if err := os.MkdirAll(s.baseDir, 0o700); err != nil {
		s.dirLock.Unlock()
		return fmt.Errorf("git token store: create auth dir: %w", err)
	}
	if err := os.MkdirAll(s.configDir, 0o700); err != nil {
		s.dirLock.Unlock()
		return fmt.Errorf("git token store: create config dir: %w", err)
	}
	s.dirLock.Unlock()
	if len(initPaths) > 0 {
		if errCommit := s.commitAndPushInitialLocked("Initialize git token store", initPaths...); errCommit != nil {
			return errCommit
		}
	}
	return nil
}

// Save persists token storage and metadata to the resolved auth file path.
func (s *GitTokenStore) Save(_ context.Context, auth *cliproxyauth.Auth) (string, error) {
	if auth == nil {
		return "", fmt.Errorf("auth filestore: auth is nil")
	}
	if errWeight := cliproxyauth.ValidateAuthWeight(auth); errWeight != nil {
		return "", fmt.Errorf("auth filestore: %w", errWeight)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	path, err := s.resolveAuthPath(auth)
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", fmt.Errorf("auth filestore: missing file path attribute for %s", auth.ID)
	}

	if auth.Disabled {
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			return "", nil
		}
	}

	if err = s.ensureRepositoryLocked(); err != nil {
		return "", err
	}
	relPath, errRel := s.relativeToRepo(path)
	if errRel != nil {
		return "", errRel
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("auth filestore: create dir failed: %w", err)
	}

	switch {
	case auth.Storage != nil:
		if auth.Metadata == nil {
			auth.Metadata = make(map[string]any)
		}
		auth.Metadata["disabled"] = auth.Disabled
		if setter, ok := auth.Storage.(interface{ SetMetadata(map[string]any) }); ok {
			setter.SetMetadata(auth.Metadata)
		}
		if err = auth.Storage.SaveTokenToFile(path); err != nil {
			return "", err
		}
	case auth.Metadata != nil:
		auth.Metadata["disabled"] = auth.Disabled
		raw, errMarshal := json.Marshal(auth.Metadata)
		if errMarshal != nil {
			return "", fmt.Errorf("auth filestore: marshal metadata failed: %w", errMarshal)
		}
		contentsMatch := false
		if existing, errRead := os.ReadFile(path); errRead == nil {
			contentsMatch = jsonEqual(existing, raw)
		} else if !os.IsNotExist(errRead) {
			return "", fmt.Errorf("auth filestore: read existing failed: %w", errRead)
		}
		if !contentsMatch {
			tmp := path + ".tmp"
			if errWrite := os.WriteFile(tmp, raw, 0o600); errWrite != nil {
				return "", fmt.Errorf("auth filestore: write temp failed: %w", errWrite)
			}
			if errRename := os.Rename(tmp, path); errRename != nil {
				return "", fmt.Errorf("auth filestore: rename failed: %w", errRename)
			}
		}
	default:
		return "", fmt.Errorf("auth filestore: nothing to persist for %s", auth.ID)
	}

	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes[cliproxyauth.AttributePath] = path
	auth.Attributes[cliproxyauth.AttributeSourceBackend] = cliproxyauth.AuthSourceGit

	if strings.TrimSpace(auth.FileName) == "" {
		auth.FileName = auth.ID
	}

	messageID := auth.ID
	if strings.TrimSpace(messageID) == "" {
		messageID = filepath.Base(path)
	}
	if errCommit := s.commitAndPushLocked(fmt.Sprintf("Update auth %s", strings.TrimSpace(messageID)), relPath); errCommit != nil {
		return "", errCommit
	}

	return path, nil
}

// List enumerates all auth JSON files under the configured directory.
func (s *GitTokenStore) List(_ context.Context) ([]*cliproxyauth.Auth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureRepositoryLocked(); err != nil {
		return nil, err
	}
	dir := s.baseDirSnapshot()
	if dir == "" {
		return nil, fmt.Errorf("auth filestore: directory not configured")
	}
	entries := make([]*cliproxyauth.Auth, 0)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
			return nil
		}
		auth, err := s.readAuthFile(path, dir)
		if err != nil {
			return nil
		}
		if auth != nil {
			entries = append(entries, auth)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// Delete removes the auth file.
func (s *GitTokenStore) Delete(_ context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("auth filestore: id is empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	path, err := s.resolveDeletePath(id)
	if err != nil {
		return err
	}
	if err = s.ensureRepositoryLocked(); err != nil {
		return err
	}
	rel, errRel := s.relativeToRepo(path)
	if errRel != nil {
		return errRel
	}
	if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("auth filestore: delete failed: %w", err)
	}
	messageID := id
	if errCommit := s.commitAndPushLocked(fmt.Sprintf("Delete auth %s", messageID), rel); errCommit != nil {
		return errCommit
	}
	return nil
}

// PersistAuthFiles commits and pushes the provided paths to the remote repository.
// It no-ops when the store is not fully configured or when there are no paths.
func (s *GitTokenStore) PersistAuthFiles(_ context.Context, message string, paths ...string) error {
	if len(paths) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	filtered := make([]string, 0, len(paths))
	for _, p := range paths {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			continue
		}
		rel, err := s.relativeToRepo(trimmed)
		if err != nil {
			return err
		}
		filtered = append(filtered, rel)
	}
	if len(filtered) == 0 {
		return nil
	}
	if strings.TrimSpace(message) == "" {
		message = "Sync watcher updates"
	}

	// Inspect watcher removals before EnsureRepository restores missing tracked
	// files so an unexpected filesystem event remains distinguishable from Delete.
	if _, errStat := os.Stat(filepath.Join(s.repoDirSnapshot(), ".git")); errStat == nil {
		if handled, errGuard := s.guardWatcherAuthRemovalLocked(message, filtered); handled || errGuard != nil {
			return errGuard
		}
	} else if !errors.Is(errStat, fs.ErrNotExist) {
		return fmt.Errorf("git token store: stat repository before watcher removal guard: %w", errStat)
	}
	if err := s.ensureRepositoryLocked(); err != nil {
		return err
	}
	if handled, errGuard := s.guardWatcherAuthRemovalLocked(message, filtered); handled || errGuard != nil {
		return errGuard
	}
	return s.commitAndPushLocked(message, filtered...)
}

func (s *GitTokenStore) guardWatcherAuthRemovalLocked(message string, relPaths []string) (bool, error) {
	if !strings.HasPrefix(strings.TrimSpace(message), "Remove auth ") {
		return false, nil
	}
	repoDir := s.repoDirSnapshot()
	if repoDir == "" {
		return true, fmt.Errorf("git token store: repository path not configured")
	}
	repo, errOpen := git.PlainOpen(repoDir)
	if errOpen != nil {
		return true, fmt.Errorf("git token store: open repo for watcher removal guard: %w", errOpen)
	}
	head, errHead := repo.Head()
	if errHead != nil {
		if errors.Is(errHead, plumbing.ErrReferenceNotFound) {
			return true, nil
		}
		return true, fmt.Errorf("git token store: inspect head for watcher removal guard: %w", errHead)
	}
	commit, errCommit := repo.CommitObject(head.Hash())
	if errCommit != nil {
		return true, fmt.Errorf("git token store: inspect commit for watcher removal guard: %w", errCommit)
	}
	tree, errTree := commit.Tree()
	if errTree != nil {
		return true, fmt.Errorf("git token store: inspect tree for watcher removal guard: %w", errTree)
	}

	hasExistingPath := false
	for _, rel := range relPaths {
		cleanRel := filepath.ToSlash(filepath.Clean(rel))
		worktreePath := filepath.Join(repoDir, filepath.FromSlash(cleanRel))
		if _, errStat := os.Stat(worktreePath); errStat == nil {
			hasExistingPath = true
			continue
		} else if !errors.Is(errStat, fs.ErrNotExist) {
			return true, fmt.Errorf("git token store: stat watcher removal path %s: %w", cleanRel, errStat)
		}

		if _, errFile := tree.File(cleanRel); errFile == nil {
			return true, fmt.Errorf("git token store: refusing watcher-originated removal of tracked auth %s; use an explicit delete", cleanRel)
		} else if !errors.Is(errFile, object.ErrFileNotFound) {
			return true, fmt.Errorf("git token store: inspect watcher removal path %s: %w", cleanRel, errFile)
		}
	}
	if hasExistingPath {
		return false, nil
	}
	// Explicit GitTokenStore.Delete already removed the path from HEAD. The
	// subsequent filesystem watcher event is therefore redundant and safe to ignore.
	return true, nil
}

func (s *GitTokenStore) resolveDeletePath(id string) (string, error) {
	if strings.ContainsRune(id, os.PathSeparator) || filepath.IsAbs(id) {
		return id, nil
	}
	dir := s.baseDirSnapshot()
	if dir == "" {
		return "", fmt.Errorf("auth filestore: directory not configured")
	}
	return filepath.Join(dir, id), nil
}

func (s *GitTokenStore) readAuthFile(path, baseDir string) (*cliproxyauth.Auth, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	metadata := make(map[string]any)
	if err = json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("unmarshal auth json: %w", err)
	}
	if errWeight := cliproxyauth.ValidateAuthWeight(&cliproxyauth.Auth{Metadata: metadata}); errWeight != nil {
		return nil, errWeight
	}
	provider, _ := metadata["type"].(string)
	if provider == "" {
		provider = "unknown"
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat file: %w", err)
	}
	id := s.idFor(path, baseDir)
	auth := &cliproxyauth.Auth{
		ID:       id,
		Provider: provider,
		FileName: id,
		Label:    s.labelFor(metadata),
		Status:   cliproxyauth.StatusActive,
		Attributes: map[string]string{
			cliproxyauth.AttributePath:          path,
			cliproxyauth.AttributeSourceBackend: cliproxyauth.AuthSourceGit,
		},
		Metadata:         metadata,
		CreatedAt:        info.ModTime(),
		UpdatedAt:        info.ModTime(),
		LastRefreshedAt:  time.Time{},
		NextRefreshAfter: time.Time{},
	}
	if email, ok := metadata["email"].(string); ok && email != "" {
		auth.Attributes["email"] = email
	}
	cliproxyauth.ApplyCustomHeadersFromMetadata(auth)
	if disabled, ok := metadata["disabled"].(bool); ok && disabled {
		auth.Disabled = true
		auth.Status = cliproxyauth.StatusDisabled
	}
	return auth, nil
}

func (s *GitTokenStore) idFor(path, baseDir string) string {
	if baseDir == "" {
		return path
	}
	rel, err := filepath.Rel(baseDir, path)
	if err != nil {
		return path
	}
	return rel
}

func (s *GitTokenStore) resolveAuthPath(auth *cliproxyauth.Auth) (string, error) {
	if auth == nil {
		return "", fmt.Errorf("auth filestore: auth is nil")
	}
	if auth.Attributes != nil {
		if p := strings.TrimSpace(auth.Attributes["path"]); p != "" {
			return p, nil
		}
	}
	if fileName := strings.TrimSpace(auth.FileName); fileName != "" {
		if filepath.IsAbs(fileName) {
			return fileName, nil
		}
		if dir := s.baseDirSnapshot(); dir != "" {
			return filepath.Join(dir, fileName), nil
		}
		return fileName, nil
	}
	if auth.ID == "" {
		return "", fmt.Errorf("auth filestore: missing id")
	}
	if filepath.IsAbs(auth.ID) {
		return auth.ID, nil
	}
	dir := s.baseDirSnapshot()
	if dir == "" {
		return "", fmt.Errorf("auth filestore: directory not configured")
	}
	return filepath.Join(dir, auth.ID), nil
}

func (s *GitTokenStore) labelFor(metadata map[string]any) string {
	if metadata == nil {
		return ""
	}
	if v, ok := metadata["label"].(string); ok && v != "" {
		return v
	}
	if v, ok := metadata["email"].(string); ok && v != "" {
		return v
	}
	if project, ok := metadata["project_id"].(string); ok && project != "" {
		return project
	}
	return ""
}

func (s *GitTokenStore) baseDirSnapshot() string {
	s.dirLock.RLock()
	defer s.dirLock.RUnlock()
	return s.baseDir
}

func (s *GitTokenStore) repoDirSnapshot() string {
	s.dirLock.RLock()
	defer s.dirLock.RUnlock()
	return s.repoDir
}

func disableGitCommitSigning(repoDir string) error {
	repo, errOpen := git.PlainOpen(repoDir)
	if errOpen != nil {
		return fmt.Errorf("git token store: open repository config: %w", errOpen)
	}
	cfg, errConfig := repo.Config()
	if errConfig != nil {
		return fmt.Errorf("git token store: get repository config: %w", errConfig)
	}
	cfg.Commit.GpgSign = config.OptBoolFalse
	if errSetConfig := repo.SetConfig(cfg); errSetConfig != nil {
		return fmt.Errorf("git token store: disable commit signing: %w", errSetConfig)
	}
	return nil
}

func (s *GitTokenStore) gitClientOptions() []client.Option {
	if s.username == "" && s.password == "" {
		return nil
	}
	user := s.username
	if user == "" {
		user = "git"
	}
	return []client.Option{client.WithHTTPAuth(&http.BasicAuth{Username: user, Password: s.password})}
}

func (s *GitTokenStore) relativeToRepo(path string) (string, error) {
	repoDir := s.repoDirSnapshot()
	if repoDir == "" {
		return "", fmt.Errorf("git token store: repository path not configured")
	}
	absRepo, errRepo := filepath.Abs(repoDir)
	if errRepo != nil {
		return "", fmt.Errorf("git token store: resolve repository path: %w", errRepo)
	}
	absPath, errPath := filepath.Abs(path)
	if errPath != nil {
		return "", fmt.Errorf("git token store: resolve path: %w", errPath)
	}
	rel, errRel := filepath.Rel(absRepo, absPath)
	if errRel != nil {
		return "", fmt.Errorf("git token store: relative path: %w", errRel)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("git token store: path outside repository")
	}
	return rel, nil
}

func (s *GitTokenStore) checkoutConfiguredBranch(repo *git.Repository, worktree *git.Worktree, authMethod []client.Option) error {
	branchRefName := plumbing.NewBranchReferenceName(s.branch)
	headRef, errHead := repo.Head()
	switch {
	case errHead == nil && headRef.Name() == branchRefName:
		return nil
	case errHead != nil && !errors.Is(errHead, plumbing.ErrReferenceNotFound):
		return fmt.Errorf("git token store: get head: %w", errHead)
	}

	if err := worktree.Checkout(&git.CheckoutOptions{Branch: branchRefName}); err == nil {
		return nil
	} else if _, errRef := repo.Reference(branchRefName, true); errRef == nil {
		return fmt.Errorf("git token store: checkout branch %s: %w", s.branch, err)
	} else if !errors.Is(errRef, plumbing.ErrReferenceNotFound) {
		return fmt.Errorf("git token store: inspect branch %s: %w", s.branch, errRef)
	} else if err := s.checkoutConfiguredRemoteTrackingBranch(repo, worktree, branchRefName, authMethod); err != nil {
		return fmt.Errorf("git token store: checkout branch %s: %w", s.branch, err)
	}

	return nil
}

func (s *GitTokenStore) checkoutConfiguredRemoteTrackingBranch(repo *git.Repository, worktree *git.Worktree, branchRefName plumbing.ReferenceName, authMethod []client.Option) error {
	remoteRefName := plumbing.ReferenceName("refs/remotes/origin/" + s.branch)
	remoteRef, err := repo.Reference(remoteRefName, true)
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		if errSync := syncRemoteReferences(repo, authMethod); errSync != nil {
			return fmt.Errorf("sync remote refs: %w", errSync)
		}
		remoteRef, err = repo.Reference(remoteRefName, true)
	}
	if err != nil {
		return err
	}
	if err := worktree.Checkout(&git.CheckoutOptions{Branch: branchRefName, Create: true, Hash: remoteRef.Hash()}); err != nil {
		return err
	}

	cfg, err := repo.Config()
	if err != nil {
		return fmt.Errorf("git token store: repo config: %w", err)
	}
	if _, ok := cfg.Branches[s.branch]; !ok {
		cfg.Branches[s.branch] = &config.Branch{Name: s.branch}
	}
	cfg.Branches[s.branch].Remote = "origin"
	cfg.Branches[s.branch].Merge = branchRefName
	if err := repo.SetConfig(cfg); err != nil {
		return fmt.Errorf("git token store: set branch config: %w", err)
	}
	return nil
}

func syncRemoteReferences(repo *git.Repository, authMethod []client.Option) error {
	if err := repo.Fetch(&git.FetchOptions{ClientOptions: authMethod, RemoteName: "origin"}); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return err
	}
	return nil
}

// resolveRemoteDefaultBranch queries the origin remote to determine the remote's default branch
// (the target of HEAD) and returns the corresponding local branch reference name (e.g. refs/heads/master).
func resolveRemoteDefaultBranch(repo *git.Repository, authMethod []client.Option) (resolvedRemoteBranch, error) {
	if err := syncRemoteReferences(repo, authMethod); err != nil {
		return resolvedRemoteBranch{}, fmt.Errorf("resolve remote default: sync remote refs: %w", err)
	}
	remote, err := repo.Remote("origin")
	if err != nil {
		return resolvedRemoteBranch{}, fmt.Errorf("resolve remote default: get remote: %w", err)
	}
	refs, err := remote.List(&git.ListOptions{ClientOptions: authMethod})
	if err != nil {
		if resolved, ok := resolveRemoteDefaultBranchFromLocal(repo); ok {
			return resolved, nil
		}
		return resolvedRemoteBranch{}, fmt.Errorf("resolve remote default: list remote refs: %w", err)
	}
	for _, r := range refs {
		if r.Name() == plumbing.HEAD {
			if r.Type() == plumbing.SymbolicReference {
				if target, ok := normalizeRemoteBranchReference(r.Target()); ok {
					return resolvedRemoteBranch{name: target}, nil
				}
			}
			s := r.String()
			if idx := strings.Index(s, "->"); idx != -1 {
				if target, ok := normalizeRemoteBranchReference(plumbing.ReferenceName(strings.TrimSpace(s[idx+2:]))); ok {
					return resolvedRemoteBranch{name: target}, nil
				}
			}
		}
	}
	if resolved, ok := resolveRemoteDefaultBranchFromLocal(repo); ok {
		return resolved, nil
	}
	for _, r := range refs {
		if normalized, ok := normalizeRemoteBranchReference(r.Name()); ok {
			return resolvedRemoteBranch{name: normalized, hash: r.Hash()}, nil
		}
	}
	return resolvedRemoteBranch{}, fmt.Errorf("resolve remote default: remote default branch not found")
}

func resolveRemoteDefaultBranchFromLocal(repo *git.Repository) (resolvedRemoteBranch, bool) {
	ref, err := repo.Reference(plumbing.ReferenceName("refs/remotes/origin/HEAD"), true)
	if err != nil || ref.Type() != plumbing.SymbolicReference {
		return resolvedRemoteBranch{}, false
	}
	target, ok := normalizeRemoteBranchReference(ref.Target())
	if !ok {
		return resolvedRemoteBranch{}, false
	}
	return resolvedRemoteBranch{name: target}, true
}

func normalizeRemoteBranchReference(name plumbing.ReferenceName) (plumbing.ReferenceName, bool) {
	switch {
	case strings.HasPrefix(name.String(), "refs/heads/"):
		return name, true
	case strings.HasPrefix(name.String(), "refs/remotes/origin/"):
		return plumbing.NewBranchReferenceName(strings.TrimPrefix(name.String(), "refs/remotes/origin/")), true
	default:
		return "", false
	}
}

func resetIndexToHead(repo *git.Repository, worktree *git.Worktree) error {
	if repo == nil || worktree == nil {
		return fmt.Errorf("repository or worktree is nil")
	}
	head, errHead := repo.Head()
	if errHead != nil {
		if errors.Is(errHead, plumbing.ErrReferenceNotFound) {
			return nil
		}
		return errHead
	}
	return worktree.Reset(&git.ResetOptions{Mode: git.MixedReset, Commit: head.Hash()})
}

func worktreeDirtyPaths(worktree *git.Worktree) (map[string]struct{}, error) {
	if worktree == nil {
		return nil, fmt.Errorf("worktree is nil")
	}
	status, errStatus := worktree.Status()
	if errStatus != nil {
		return nil, errStatus
	}
	dirtyPaths := make(map[string]struct{}, len(status))
	for path, fileStatus := range status {
		if fileStatus.Staging == git.Unmodified && fileStatus.Worktree == git.Unmodified {
			continue
		}
		dirtyPaths[filepath.ToSlash(filepath.Clean(path))] = struct{}{}
	}
	return dirtyPaths, nil
}

func reconcileRemoteWorktree(repo *git.Repository, worktree *git.Worktree, repoDir string, baseRef *plumbing.Reference, dirtyPaths map[string]struct{}) error {
	if repo == nil || worktree == nil || baseRef == nil {
		return fmt.Errorf("repository, worktree, or base reference is nil")
	}
	if !baseRef.Name().IsBranch() {
		return fmt.Errorf("head %s is not a branch", baseRef.Name())
	}
	remoteName := plumbing.NewRemoteReferenceName("origin", baseRef.Name().Short())
	remoteRef, errRemote := repo.Reference(remoteName, true)
	if errRemote != nil {
		return fmt.Errorf("resolve remote branch %s: %w", remoteName, errRemote)
	}
	baseCommit, errBaseCommit := repo.CommitObject(baseRef.Hash())
	if errBaseCommit != nil {
		return fmt.Errorf("inspect pre-pull commit: %w", errBaseCommit)
	}
	baseTree, errBaseTree := baseCommit.Tree()
	if errBaseTree != nil {
		return fmt.Errorf("inspect pre-pull tree: %w", errBaseTree)
	}
	remoteCommit, errRemoteCommit := repo.CommitObject(remoteRef.Hash())
	if errRemoteCommit != nil {
		return fmt.Errorf("inspect remote commit: %w", errRemoteCommit)
	}
	remoteTree, errRemoteTree := remoteCommit.Tree()
	if errRemoteTree != nil {
		return fmt.Errorf("inspect remote tree: %w", errRemoteTree)
	}
	changedPaths, errChangedPaths := changedTreePaths(baseTree, remoteTree)
	if errChangedPaths != nil {
		return errChangedPaths
	}
	for _, changedPath := range changedPaths {
		if dirtyPath, conflict := overlappingDirtyPath(changedPath, dirtyPaths); conflict {
			if errRestore := restoreHeadAndIndex(repo, worktree, baseRef); errRestore != nil {
				return errors.Join(
					fmt.Errorf("remote path %s conflicts with local change %s", changedPath, dirtyPath),
					fmt.Errorf("restore pre-pull head after conflict: %w", errRestore),
				)
			}
			return fmt.Errorf("remote path %s conflicts with local change %s", changedPath, dirtyPath)
		}
	}

	// Pull moves HEAD before reporting unstaged changes. Return to the pre-pull
	// tree before applying only remote changes that do not overlap local edits.
	if errRestore := restoreHeadAndIndex(repo, worktree, baseRef); errRestore != nil {
		return fmt.Errorf("restore pre-pull head: %w", errRestore)
	}
	if errApply := applyTreePaths(remoteTree, repoDir, changedPaths); errApply != nil {
		if errRollback := applyTreePaths(baseTree, repoDir, changedPaths); errRollback != nil {
			return errors.Join(
				fmt.Errorf("apply remote worktree changes: %w", errApply),
				fmt.Errorf("restore pre-pull worktree: %w", errRollback),
			)
		}
		return fmt.Errorf("apply remote worktree changes: %w", errApply)
	}
	if errReference := repo.Storer.SetReference(plumbing.NewHashReference(baseRef.Name(), remoteRef.Hash())); errReference != nil {
		if errRollback := applyTreePaths(baseTree, repoDir, changedPaths); errRollback != nil {
			return errors.Join(
				fmt.Errorf("update branch %s: %w", baseRef.Name(), errReference),
				fmt.Errorf("restore pre-pull worktree: %w", errRollback),
			)
		}
		return fmt.Errorf("update branch %s: %w", baseRef.Name(), errReference)
	}
	if errReset := worktree.Reset(&git.ResetOptions{Mode: git.MixedReset, Commit: remoteRef.Hash()}); errReset != nil {
		return fmt.Errorf("reset index to remote branch %s: %w", remoteName, errReset)
	}
	return nil
}

func changedTreePaths(baseTree, remoteTree *object.Tree) ([]string, error) {
	changes, errDiff := baseTree.Diff(remoteTree)
	if errDiff != nil {
		return nil, fmt.Errorf("compare pre-pull and remote trees: %w", errDiff)
	}
	paths := make(map[string]struct{}, len(changes))
	for _, change := range changes {
		for _, path := range []string{change.From.Name, change.To.Name} {
			if path == "" {
				continue
			}
			paths[filepath.ToSlash(filepath.Clean(path))] = struct{}{}
		}
	}
	changedPaths := make([]string, 0, len(paths))
	for path := range paths {
		changedPaths = append(changedPaths, path)
	}
	sort.Strings(changedPaths)
	return changedPaths, nil
}

func overlappingDirtyPath(path string, dirtyPaths map[string]struct{}) (string, bool) {
	for dirtyPath := range dirtyPaths {
		if path == dirtyPath || strings.HasPrefix(path, dirtyPath+"/") || strings.HasPrefix(dirtyPath, path+"/") {
			return dirtyPath, true
		}
	}
	return "", false
}

func applyTreePaths(tree *object.Tree, repoDir string, paths []string) error {
	for _, path := range paths {
		destination := filepath.Join(repoDir, filepath.FromSlash(path))
		file, errFile := tree.File(path)
		if errors.Is(errFile, object.ErrFileNotFound) {
			if errRemove := os.Remove(destination); errRemove != nil && !errors.Is(errRemove, fs.ErrNotExist) {
				return fmt.Errorf("remove %s: %w", path, errRemove)
			}
			continue
		}
		if errFile != nil {
			return fmt.Errorf("inspect %s: %w", path, errFile)
		}
		contents, errContents := file.Contents()
		if errContents != nil {
			return fmt.Errorf("read %s: %w", path, errContents)
		}
		if errMkdir := os.MkdirAll(filepath.Dir(destination), 0o700); errMkdir != nil {
			return fmt.Errorf("create parent for %s: %w", path, errMkdir)
		}
		if errWrite := os.WriteFile(destination, []byte(contents), 0o600); errWrite != nil {
			return fmt.Errorf("write %s: %w", path, errWrite)
		}
	}
	return nil
}

func (s *GitTokenStore) recoverRepositoryLocked(repoDir string, authMethod []client.Option, baselineTree *object.Tree, dirtyPaths map[string]struct{}) (errRecovery error) {
	parentDir := filepath.Dir(repoDir)
	recoveryRoot, errTemp := os.MkdirTemp(parentDir, ".gitstore-recovery-")
	if errTemp != nil {
		return fmt.Errorf("create recovery directory: %w", errTemp)
	}
	cleanupRecovery := true
	defer func() {
		if !cleanupRecovery {
			return
		}
		if errRemove := os.RemoveAll(recoveryRoot); errRemove != nil {
			errCleanup := fmt.Errorf("remove recovery directory: %w", errRemove)
			if errRecovery == nil {
				errRecovery = errCleanup
			} else {
				errRecovery = errors.Join(errRecovery, errCleanup)
			}
		}
	}()

	if baselineTree == nil {
		inspectedTree, inspectedDirtyPaths, errInspect := inspectRecoveryBaseline(repoDir)
		if errInspect != nil {
			return fmt.Errorf("inspect recovery baseline: %w", errInspect)
		}
		baselineTree = inspectedTree
		dirtyPaths = inspectedDirtyPaths
	}
	cloneDir := filepath.Join(recoveryRoot, "clone")
	cloneOpts := &git.CloneOptions{ClientOptions: authMethod, URL: s.remote}
	if s.branch != "" {
		cloneOpts.ReferenceName = plumbing.NewBranchReferenceName(s.branch)
	}
	clonedRepo, errClone := git.PlainClone(cloneDir, cloneOpts)
	if errClone != nil {
		return fmt.Errorf("clone remote repository: %w", errClone)
	}
	if errVerify := verifyRepositoryHead(clonedRepo); errVerify != nil {
		return fmt.Errorf("verify cloned repository: %w", errVerify)
	}
	clonedHead, errHead := clonedRepo.Head()
	if errHead != nil {
		return fmt.Errorf("get cloned repository head: %w", errHead)
	}
	clonedCommit, errCommit := clonedRepo.CommitObject(clonedHead.Hash())
	if errCommit != nil {
		return fmt.Errorf("inspect cloned repository head: %w", errCommit)
	}
	remoteTree, errTree := clonedCommit.Tree()
	if errTree != nil {
		return fmt.Errorf("inspect cloned repository tree: %w", errTree)
	}
	preservedPaths, errPreserve := recoveryPreservedPaths(baselineTree, remoteTree, dirtyPaths)
	if errPreserve != nil {
		return errPreserve
	}
	if errApply := applyRecoveryLocalChanges(repoDir, cloneDir, preservedPaths); errApply != nil {
		return fmt.Errorf("preserve local worktree changes: %w", errApply)
	}

	backupWorktreeDir := filepath.Join(recoveryRoot, "worktree")
	if errBackup := moveWorktreeEntries(repoDir, backupWorktreeDir); errBackup != nil {
		return fmt.Errorf("backup existing worktree: %w", errBackup)
	}
	gitDir := filepath.Join(repoDir, ".git")
	clonedGitDir := filepath.Join(cloneDir, ".git")
	backupGitDir := filepath.Join(recoveryRoot, "corrupt.git")
	retainRecovery, errInstall := installRecoveredGitDirectory(gitDir, clonedGitDir, backupGitDir, os.Rename)
	if retainRecovery {
		cleanupRecovery = false
	}
	if errInstall != nil {
		if errRestore := moveWorktreeEntries(backupWorktreeDir, repoDir); errRestore != nil {
			cleanupRecovery = false
			return errors.Join(errInstall, fmt.Errorf("restore worktree; backup retained at %s: %w", backupWorktreeDir, errRestore))
		}
		return errInstall
	}
	if errMove := moveWorktreeEntries(cloneDir, repoDir); errMove != nil {
		errMoveWorktree := fmt.Errorf("install recovered worktree: %w", errMove)
		if errRollback := rollbackRecoveredRepository(repoDir, gitDir, backupGitDir, backupWorktreeDir); errRollback != nil {
			cleanupRecovery = false
			return errors.Join(errMoveWorktree, fmt.Errorf("rollback recovered repository; backup retained at %s: %w", recoveryRoot, errRollback))
		}
		return errMoveWorktree
	}
	recoveredRepo, errOpen := git.PlainOpen(repoDir)
	if errOpen == nil {
		errOpen = verifyRepositoryHead(recoveredRepo)
	}
	if errOpen != nil {
		errRecovered := fmt.Errorf("verify recovered repository: %w", errOpen)
		if errRollback := rollbackRecoveredRepository(repoDir, gitDir, backupGitDir, backupWorktreeDir); errRollback != nil {
			cleanupRecovery = false
			return errors.Join(errRecovered, fmt.Errorf("rollback recovered repository; backup retained at %s: %w", recoveryRoot, errRollback))
		}
		return errRecovered
	}
	return nil
}

func inspectRecoveryBaseline(repoDir string) (*object.Tree, map[string]struct{}, error) {
	repo, errOpen := git.PlainOpen(repoDir)
	if errOpen != nil {
		return nil, nil, fmt.Errorf("open repository: %w", errOpen)
	}
	worktree, errWorktree := repo.Worktree()
	if errWorktree != nil {
		return nil, nil, fmt.Errorf("open worktree: %w", errWorktree)
	}
	dirtyPaths, errDirty := worktreeDirtyPaths(worktree)
	if errDirty != nil {
		return nil, nil, fmt.Errorf("inspect worktree changes: %w", errDirty)
	}
	head, errHead := repo.Head()
	if errHead != nil {
		return nil, nil, fmt.Errorf("inspect head: %w", errHead)
	}
	commit, errCommit := repo.CommitObject(head.Hash())
	if errCommit != nil {
		return nil, nil, fmt.Errorf("inspect head commit: %w", errCommit)
	}
	tree, errTree := commit.Tree()
	if errTree != nil {
		return nil, nil, fmt.Errorf("inspect head tree: %w", errTree)
	}
	return tree, dirtyPaths, nil
}

func recoveryPreservedPaths(baselineTree, remoteTree *object.Tree, dirtyPaths map[string]struct{}) (map[string]struct{}, error) {
	if baselineTree == nil || len(dirtyPaths) == 0 {
		return nil, nil
	}
	changedPaths, errChanged := changedTreePaths(baselineTree, remoteTree)
	if errChanged != nil {
		return nil, fmt.Errorf("verify local changes against recovered remote: %w", errChanged)
	}
	for _, changedPath := range changedPaths {
		if dirtyPath, conflict := overlappingDirtyPath(changedPath, dirtyPaths); conflict {
			return nil, fmt.Errorf("remote path %s conflicts with local change %s during repository recovery", changedPath, dirtyPath)
		}
	}
	return dirtyPaths, nil
}

func applyRecoveryLocalChanges(sourceDir, targetDir string, paths map[string]struct{}) error {
	sortedPaths := make([]string, 0, len(paths))
	for path := range paths {
		sortedPaths = append(sortedPaths, path)
	}
	sort.Strings(sortedPaths)
	for _, path := range sortedPaths {
		source := filepath.Join(sourceDir, filepath.FromSlash(path))
		target := filepath.Join(targetDir, filepath.FromSlash(path))
		info, errStat := os.Lstat(source)
		if errors.Is(errStat, fs.ErrNotExist) {
			if errRemove := os.RemoveAll(target); errRemove != nil {
				return fmt.Errorf("preserve deletion %s: %w", path, errRemove)
			}
			continue
		}
		if errStat != nil {
			return fmt.Errorf("inspect local change %s: %w", path, errStat)
		}
		if errRemove := os.RemoveAll(target); errRemove != nil {
			return fmt.Errorf("replace recovered path %s: %w", path, errRemove)
		}
		if errMkdir := os.MkdirAll(filepath.Dir(target), 0o700); errMkdir != nil {
			return fmt.Errorf("create recovered parent for %s: %w", path, errMkdir)
		}
		switch {
		case info.Mode().IsRegular():
			contents, errRead := os.ReadFile(source)
			if errRead != nil {
				return fmt.Errorf("read local change %s: %w", path, errRead)
			}
			if errWrite := os.WriteFile(target, contents, info.Mode().Perm()); errWrite != nil {
				return fmt.Errorf("write local change %s: %w", path, errWrite)
			}
		case info.Mode()&os.ModeSymlink != 0:
			linkTarget, errReadlink := os.Readlink(source)
			if errReadlink != nil {
				return fmt.Errorf("read local symlink %s: %w", path, errReadlink)
			}
			if errSymlink := os.Symlink(linkTarget, target); errSymlink != nil {
				return fmt.Errorf("write local symlink %s: %w", path, errSymlink)
			}
		default:
			return fmt.Errorf("local change %s has unsupported file mode %s", path, info.Mode())
		}
	}
	return nil
}

func moveWorktreeEntries(sourceDir, targetDir string) error {
	if errMkdir := os.MkdirAll(targetDir, 0o700); errMkdir != nil {
		return errMkdir
	}
	entries, errRead := os.ReadDir(sourceDir)
	if errRead != nil {
		return errRead
	}
	moved := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		source := filepath.Join(sourceDir, entry.Name())
		target := filepath.Join(targetDir, entry.Name())
		if errRename := os.Rename(source, target); errRename != nil {
			errMove := fmt.Errorf("move %s: %w", entry.Name(), errRename)
			for index := len(moved) - 1; index >= 0; index-- {
				name := moved[index]
				if errRestore := os.Rename(filepath.Join(targetDir, name), filepath.Join(sourceDir, name)); errRestore != nil {
					errMove = errors.Join(errMove, fmt.Errorf("restore %s: %w", name, errRestore))
				}
			}
			return errMove
		}
		moved = append(moved, entry.Name())
	}
	return nil
}

func removeWorktreeEntries(repoDir string) error {
	entries, errRead := os.ReadDir(repoDir)
	if errRead != nil {
		return errRead
	}
	for _, entry := range entries {
		if entry.Name() == ".git" {
			continue
		}
		if errRemove := os.RemoveAll(filepath.Join(repoDir, entry.Name())); errRemove != nil {
			return errRemove
		}
	}
	return nil
}

func rollbackRecoveredRepository(repoDir, gitDir, backupGitDir, backupWorktreeDir string) error {
	if errRemove := removeWorktreeEntries(repoDir); errRemove != nil {
		return fmt.Errorf("remove recovered worktree: %w", errRemove)
	}
	if errRollback := rollbackRecoveredGitDirectory(gitDir, backupGitDir); errRollback != nil {
		return errRollback
	}
	if errRestore := moveWorktreeEntries(backupWorktreeDir, repoDir); errRestore != nil {
		return fmt.Errorf("restore original worktree: %w", errRestore)
	}
	return nil
}

func installRecoveredGitDirectory(gitDir, clonedGitDir, backupGitDir string, rename func(string, string) error) (bool, error) {
	if errRename := rename(gitDir, backupGitDir); errRename != nil {
		return false, fmt.Errorf("backup corrupt git directory: %w", errRename)
	}
	if errRename := rename(clonedGitDir, gitDir); errRename != nil {
		if errRestore := rename(backupGitDir, gitDir); errRestore != nil {
			return true, errors.Join(
				fmt.Errorf("install recovered git directory: %w", errRename),
				fmt.Errorf("restore corrupt git directory; backup retained at %s: %w", backupGitDir, errRestore),
			)
		}
		return false, fmt.Errorf("install recovered git directory: %w", errRename)
	}
	return false, nil
}

func rollbackRecoveredGitDirectory(gitDir, backupGitDir string) error {
	if errRemove := os.RemoveAll(gitDir); errRemove != nil {
		return fmt.Errorf("remove recovered git directory: %w", errRemove)
	}
	if errRename := os.Rename(backupGitDir, gitDir); errRename != nil {
		return fmt.Errorf("restore original git directory: %w", errRename)
	}
	return nil
}

func isRepositoryCorruptionError(err error) bool {
	return errors.Is(err, dotgit.ErrPackfileNotFound) || errors.Is(err, plumbing.ErrObjectNotFound)
}

func verifyRepositoryHead(repo *git.Repository) error {
	if repo == nil {
		return fmt.Errorf("repository is nil")
	}
	head, errHead := repo.Head()
	if errHead != nil {
		if errors.Is(errHead, plumbing.ErrReferenceNotFound) {
			return nil
		}
		return errHead
	}
	commit, errCommit := repo.CommitObject(head.Hash())
	if errCommit != nil {
		return errCommit
	}
	tree, errTree := commit.Tree()
	if errTree != nil {
		return errTree
	}
	files := tree.Files()
	return files.ForEach(func(file *object.File) error {
		_, errContents := file.Contents()
		return errContents
	})
}

func restoreMissingTrackedFiles(repo *git.Repository, repoDir string) error {
	if repo == nil {
		return fmt.Errorf("repository is nil")
	}
	head, errHead := repo.Head()
	if errHead != nil {
		if errors.Is(errHead, plumbing.ErrReferenceNotFound) {
			return nil
		}
		return errHead
	}
	commit, errCommit := repo.CommitObject(head.Hash())
	if errCommit != nil {
		return errCommit
	}
	tree, errTree := commit.Tree()
	if errTree != nil {
		return errTree
	}
	files := tree.Files()
	return files.ForEach(func(file *object.File) error {
		destination := filepath.Join(repoDir, filepath.FromSlash(file.Name))
		if _, errStat := os.Lstat(destination); errStat == nil {
			return nil
		} else if !errors.Is(errStat, fs.ErrNotExist) {
			return errStat
		}
		contents, errContents := file.Contents()
		if errContents != nil {
			return errContents
		}
		if errMkdir := os.MkdirAll(filepath.Dir(destination), 0o700); errMkdir != nil {
			return errMkdir
		}
		return os.WriteFile(destination, []byte(contents), 0o600)
	})
}

func shouldFallbackToCurrentBranch(repo *git.Repository, err error) bool {
	if !errors.Is(err, transport.ErrAuthenticationRequired) && !errors.Is(err, transport.ErrEmptyRemoteRepository) {
		return false
	}
	_, headErr := repo.Head()
	return headErr == nil
}

// checkoutRemoteDefaultBranch ensures the working tree is checked out to the remote's default branch
// (the branch target of origin/HEAD). If the local branch does not exist it will be created to track
// the remote branch.
func checkoutRemoteDefaultBranch(repo *git.Repository, worktree *git.Worktree, authMethod []client.Option) error {
	resolved, err := resolveRemoteDefaultBranch(repo, authMethod)
	if err != nil {
		return err
	}
	branchRefName := resolved.name
	// If HEAD already points to the desired branch, nothing to do.
	headRef, errHead := repo.Head()
	if errHead == nil && headRef.Name() == branchRefName {
		return nil
	}
	// If local branch exists, attempt a checkout
	if _, err := repo.Reference(branchRefName, true); err == nil {
		if err := worktree.Checkout(&git.CheckoutOptions{Branch: branchRefName}); err != nil {
			return fmt.Errorf("checkout branch %s: %w", branchRefName.String(), err)
		}
		return nil
	}
	// Try to find the corresponding remote tracking ref (refs/remotes/origin/<name>)
	branchShort := strings.TrimPrefix(branchRefName.String(), "refs/heads/")
	remoteRefName := plumbing.ReferenceName("refs/remotes/origin/" + branchShort)
	hash := resolved.hash
	if remoteRef, err := repo.Reference(remoteRefName, true); err == nil {
		hash = remoteRef.Hash()
	} else if err != nil && !errors.Is(err, plumbing.ErrReferenceNotFound) {
		return fmt.Errorf("checkout remote default: remote ref %s: %w", remoteRefName.String(), err)
	}
	if hash == plumbing.ZeroHash {
		return fmt.Errorf("checkout remote default: remote ref %s not found", remoteRefName.String())
	}
	if err := worktree.Checkout(&git.CheckoutOptions{Branch: branchRefName, Create: true, Hash: hash}); err != nil {
		return fmt.Errorf("checkout create branch %s: %w", branchRefName.String(), err)
	}
	cfg, err := repo.Config()
	if err != nil {
		return fmt.Errorf("git token store: repo config: %w", err)
	}
	if _, ok := cfg.Branches[branchShort]; !ok {
		cfg.Branches[branchShort] = &config.Branch{Name: branchShort}
	}
	cfg.Branches[branchShort].Remote = "origin"
	cfg.Branches[branchShort].Merge = branchRefName
	if err := repo.SetConfig(cfg); err != nil {
		return fmt.Errorf("git token store: set branch config: %w", err)
	}
	return nil
}

func (s *GitTokenStore) commitAndPushLocked(message string, relPaths ...string) error {
	return s.commitAndPushWithOptionsLocked(message, false, relPaths...)
}

func (s *GitTokenStore) commitAndPushInitialLocked(message string, relPaths ...string) error {
	return s.commitAndPushWithOptionsLocked(message, true, relPaths...)
}

func (s *GitTokenStore) commitAndPushWithOptionsLocked(message string, allowMissingRemote bool, relPaths ...string) error {
	repoDir := s.repoDirSnapshot()
	if repoDir == "" {
		return fmt.Errorf("git token store: repository path not configured")
	}
	repo, err := git.PlainOpen(repoDir)
	if err != nil {
		return fmt.Errorf("git token store: open repo: %w", err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("git token store: worktree: %w", err)
	}
	managedPaths, errPaths := normalizeManagedPaths(relPaths)
	if errPaths != nil {
		return fmt.Errorf("git token store: validate commit paths: %w", errPaths)
	}
	if len(managedPaths) == 0 {
		return nil
	}

	baseRef, errHead := repo.Head()
	if errHead != nil && !errors.Is(errHead, plumbing.ErrReferenceNotFound) {
		return fmt.Errorf("git token store: get base head: %w", errHead)
	}
	if errHead == nil {
		if errReset := resetIndexToHead(repo, worktree); errReset != nil {
			return fmt.Errorf("git token store: reset index before commit: %w", errReset)
		}
	}

	added := false
	for _, rel := range managedPaths {
		if _, err = worktree.Add(rel); err != nil {
			if errors.Is(err, gitindex.ErrEntryNotFound) {
				continue
			}
			if errors.Is(err, os.ErrNotExist) {
				if _, errRemove := worktree.Remove(rel); errRemove != nil {
					if errors.Is(errRemove, os.ErrNotExist) || errors.Is(errRemove, gitindex.ErrEntryNotFound) {
						continue
					}
					return fmt.Errorf("git token store: remove %s: %w", rel, errRemove)
				}
			} else {
				return fmt.Errorf("git token store: add %s: %w", rel, err)
			}
		}
		added = true
	}
	if !added {
		return nil
	}
	status, err := worktree.Status()
	if err != nil {
		return fmt.Errorf("git token store: status: %w", err)
	}
	if status.IsClean() {
		return nil
	}
	if strings.TrimSpace(message) == "" {
		message = "Update auth store"
	}
	signature := &object.Signature{
		Name:  "CLIProxyAPI",
		Email: "cliproxy@local",
		When:  time.Now(),
	}
	commitHash, err := worktree.Commit(message, &git.CommitOptions{
		Author: signature,
	})
	if err != nil {
		if errors.Is(err, git.ErrEmptyCommit) {
			return nil
		}
		return fmt.Errorf("git token store: commit: %w", err)
	}
	if baseRef != nil {
		if errValidate := validateManagedTreeChanges(repo, baseRef.Hash(), commitHash, managedPaths); errValidate != nil {
			errRestore := restoreHeadAndIndex(repo, worktree, baseRef)
			if errRestore != nil {
				return errors.Join(
					fmt.Errorf("git token store: validate commit tree: %w", errValidate),
					fmt.Errorf("git token store: restore head after rejected commit: %w", errRestore),
				)
			}
			return fmt.Errorf("git token store: validate commit tree: %w", errValidate)
		}
	}
	headRef, errCommittedHead := repo.Head()
	if errCommittedHead != nil {
		return fmt.Errorf("git token store: get committed head: %w", errCommittedHead)
	}
	if errRewrite := s.rewriteHeadAsSingleCommit(repo, headRef.Name(), commitHash, message, signature); errRewrite != nil {
		return errRewrite
	}
	if errPush := s.pushRepositoryLocked(repo, repoDir, allowMissingRemote); errPush != nil {
		if baseRef == nil {
			return errPush
		}
		if errRestore := restoreHeadAndIndex(repo, worktree, baseRef); errRestore != nil {
			return errors.Join(errPush, fmt.Errorf("git token store: restore head after rejected push: %w", errRestore))
		}
		return errPush
	}
	return nil
}

func normalizeManagedPaths(paths []string) ([]string, error) {
	normalized := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" {
			continue
		}
		clean := filepath.ToSlash(filepath.Clean(trimmed))
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || filepath.IsAbs(trimmed) {
			return nil, fmt.Errorf("path %q is not a repository-relative file", path)
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		normalized = append(normalized, clean)
	}
	return normalized, nil
}

func validateManagedTreeChanges(repo *git.Repository, baseHash, commitHash plumbing.Hash, managedPaths []string) error {
	baseCommit, errBase := repo.CommitObject(baseHash)
	if errBase != nil {
		return fmt.Errorf("inspect base commit: %w", errBase)
	}
	baseTree, errBaseTree := baseCommit.Tree()
	if errBaseTree != nil {
		return fmt.Errorf("inspect base tree: %w", errBaseTree)
	}
	commit, errCommit := repo.CommitObject(commitHash)
	if errCommit != nil {
		return fmt.Errorf("inspect candidate commit: %w", errCommit)
	}
	candidateTree, errCandidateTree := commit.Tree()
	if errCandidateTree != nil {
		return fmt.Errorf("inspect candidate tree: %w", errCandidateTree)
	}
	changes, errDiff := baseTree.Diff(candidateTree)
	if errDiff != nil {
		return fmt.Errorf("compare candidate tree: %w", errDiff)
	}
	for _, change := range changes {
		for _, changedPath := range []string{change.From.Name, change.To.Name} {
			if changedPath == "" || isManagedTreePath(changedPath, managedPaths) {
				continue
			}
			return fmt.Errorf("unexpected indexed change outside requested paths: %s", changedPath)
		}
	}
	return nil
}

func isManagedTreePath(path string, managedPaths []string) bool {
	cleanPath := filepath.ToSlash(filepath.Clean(path))
	for _, managedPath := range managedPaths {
		if cleanPath == managedPath || strings.HasPrefix(cleanPath, managedPath+"/") {
			return true
		}
	}
	return false
}

func restoreHeadAndIndex(repo *git.Repository, worktree *git.Worktree, head *plumbing.Reference) error {
	if repo == nil || worktree == nil || head == nil {
		return fmt.Errorf("repository, worktree, or head is nil")
	}
	if errReference := repo.Storer.SetReference(plumbing.NewHashReference(head.Name(), head.Hash())); errReference != nil {
		return errReference
	}
	return worktree.Reset(&git.ResetOptions{Mode: git.MixedReset, Commit: head.Hash()})
}

func (s *GitTokenStore) pushRepositoryLocked(repo *git.Repository, repoDir string, allowMissingRemote bool) error {
	if repo == nil {
		return fmt.Errorf("git token store: repository is nil")
	}
	headRef, errHead := repo.Head()
	if errHead != nil {
		if errors.Is(errHead, plumbing.ErrReferenceNotFound) {
			return nil
		}
		return fmt.Errorf("git token store: get head for push: %w", errHead)
	}
	if !headRef.Name().IsBranch() {
		return fmt.Errorf("git token store: head %s is not a branch", headRef.Name())
	}
	branchName := headRef.Name()
	remoteName := plumbing.NewRemoteReferenceName("origin", branchName.Short())
	pushOpts := &git.PushOptions{
		ClientOptions: s.gitClientOptions(),
		RefSpecs:      []config.RefSpec{config.RefSpec(branchName.String() + ":" + branchName.String())},
	}
	remoteRef, errRemote := repo.Reference(remoteName, true)
	switch {
	case errRemote == nil:
		pushOpts.ForceWithLease = &git.ForceWithLease{RefName: branchName, Hash: remoteRef.Hash()}
	case errors.Is(errRemote, plumbing.ErrReferenceNotFound) && allowMissingRemote:
		// A normal branch-creation push fails if another initializer wins the race.
	case errors.Is(errRemote, plumbing.ErrReferenceNotFound):
		return fmt.Errorf("git token store: remote tracking branch %s not found", remoteName)
	default:
		return fmt.Errorf("git token store: inspect remote tracking branch %s: %w", remoteName, errRemote)
	}
	if errPush := repo.Push(pushOpts); errPush != nil {
		if !errors.Is(errPush, git.NoErrAlreadyUpToDate) {
			return fmt.Errorf("git token store: push: %w", errPush)
		}
	}
	if errReference := repo.Storer.SetReference(plumbing.NewHashReference(remoteName, headRef.Hash())); errReference != nil {
		return fmt.Errorf("git token store: update remote tracking branch %s: %w", remoteName, errReference)
	}
	s.maybeRunGC(repoDir)
	return nil
}

// rewriteHeadAsSingleCommit rewrites the current branch tip to a single-parentless commit and leaves history squashed.
func (s *GitTokenStore) rewriteHeadAsSingleCommit(repo *git.Repository, branch plumbing.ReferenceName, commitHash plumbing.Hash, message string, signature *object.Signature) error {
	commitObj, err := repo.CommitObject(commitHash)
	if err != nil {
		return fmt.Errorf("git token store: inspect head commit: %w", err)
	}
	squashed := &object.Commit{
		Author:       *signature,
		Committer:    *signature,
		Message:      message,
		TreeHash:     commitObj.TreeHash,
		ParentHashes: nil,
		Encoding:     commitObj.Encoding,
		ExtraHeaders: commitObj.ExtraHeaders,
	}
	mem := &plumbing.MemoryObject{}
	mem.SetType(plumbing.CommitObject)
	if err := squashed.Encode(mem); err != nil {
		return fmt.Errorf("git token store: encode squashed commit: %w", err)
	}
	newHash, err := repo.Storer.SetEncodedObject(mem)
	if err != nil {
		return fmt.Errorf("git token store: write squashed commit: %w", err)
	}
	if err := repo.Storer.SetReference(plumbing.NewHashReference(branch, newHash)); err != nil {
		return fmt.Errorf("git token store: update branch reference: %w", err)
	}
	return nil
}

func (s *GitTokenStore) maybeRunGC(repoDir string) {
	now := time.Now()
	if now.Sub(s.lastGC) < gcInterval {
		return
	}
	s.lastGC = now

	repo, err := git.PlainOpen(repoDir)
	if err != nil {
		return
	}

	pruneOpts := git.PruneOptions{
		OnlyObjectsOlderThan: now.Add(-gcPruneGracePeriod),
		Handler:              repo.DeleteObject,
	}
	if err := repo.Prune(pruneOpts); err != nil && !errors.Is(err, git.ErrLooseObjectsNotSupported) {
		return
	}
	_ = repo.RepackObjects(&git.RepackConfig{})
}

// PersistConfig commits and pushes configuration changes to git.
func (s *GitTokenStore) PersistConfig(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureRepositoryLocked(); err != nil {
		return err
	}
	configPath := s.ConfigPath()
	if configPath == "" {
		return fmt.Errorf("git token store: config path not configured")
	}
	if _, err := os.Stat(configPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("git token store: stat config: %w", err)
	}
	rel, err := s.relativeToRepo(configPath)
	if err != nil {
		return err
	}
	return s.commitAndPushLocked("Update config", rel)
}

func ensureEmptyFile(path string) error {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return os.WriteFile(path, []byte{}, 0o600)
		}
		return err
	}
	return nil
}

func jsonEqual(a, b []byte) bool {
	var objA any
	var objB any
	if err := json.Unmarshal(a, &objA); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &objB); err != nil {
		return false
	}
	return deepEqualJSON(objA, objB)
}

func deepEqualJSON(a, b any) bool {
	switch valA := a.(type) {
	case map[string]any:
		valB, ok := b.(map[string]any)
		if !ok || len(valA) != len(valB) {
			return false
		}
		for key, subA := range valA {
			subB, ok1 := valB[key]
			if !ok1 || !deepEqualJSON(subA, subB) {
				return false
			}
		}
		return true
	case []any:
		sliceB, ok := b.([]any)
		if !ok || len(valA) != len(sliceB) {
			return false
		}
		for i := range valA {
			if !deepEqualJSON(valA[i], sliceB[i]) {
				return false
			}
		}
		return true
	case float64:
		valB, ok := b.(float64)
		if !ok {
			return false
		}
		return valA == valB
	case string:
		valB, ok := b.(string)
		if !ok {
			return false
		}
		return valA == valB
	case bool:
		valB, ok := b.(bool)
		if !ok {
			return false
		}
		return valA == valB
	case nil:
		return b == nil
	default:
		return false
	}
}
