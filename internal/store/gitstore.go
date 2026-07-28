package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// gcInterval defines minimum time between garbage collection runs.
const gcInterval = 5 * time.Minute

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
		} else {
			s.dirLock.Unlock()
			return fmt.Errorf("git token store: stat repo: %w", err)
		}
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
		if errPull := worktree.Pull(pullOpts); errPull != nil {
			switch {
			case errors.Is(errPull, git.NoErrAlreadyUpToDate),
				errors.Is(errPull, git.ErrUnstagedChanges),
				errors.Is(errPull, git.ErrNonFastForwardUpdate):
				// Ignore clean syncs, local edits, and remote divergence—local changes win.
			case errors.Is(errPull, transport.ErrAuthenticationRequired),
				errors.Is(errPull, transport.ErrEmptyRemoteRepository):
				// Ignore authentication prompts and empty remote references on initial sync.
			case errors.Is(errPull, plumbing.ErrReferenceNotFound):
				if s.branch != "" {
					s.dirLock.Unlock()
					return fmt.Errorf("git token store: pull: %w", errPull)
				}
				// Ignore missing references only when following the remote default branch.
			default:
				s.dirLock.Unlock()
				return fmt.Errorf("git token store: pull: %w", errPull)
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
		s.mu.Lock()
		err := s.commitAndPushLocked("Initialize git token store", initPaths...)
		s.mu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

// Save persists token storage and metadata to the resolved auth file path.
func (s *GitTokenStore) Save(_ context.Context, auth *cliproxyauth.Auth) (string, error) {
	if auth == nil {
		return "", fmt.Errorf("auth filestore: auth is nil")
	}

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

	if err = s.EnsureRepository(); err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

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
		if existing, errRead := os.ReadFile(path); errRead == nil {
			if jsonEqual(existing, raw) {
				return path, nil
			}
		} else if !os.IsNotExist(errRead) {
			return "", fmt.Errorf("auth filestore: read existing failed: %w", errRead)
		}
		tmp := path + ".tmp"
		if errWrite := os.WriteFile(tmp, raw, 0o600); errWrite != nil {
			return "", fmt.Errorf("auth filestore: write temp failed: %w", errWrite)
		}
		if errRename := os.Rename(tmp, path); errRename != nil {
			return "", fmt.Errorf("auth filestore: rename temp: %w", errRename)
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

	relPath, errRel := s.relativeToRepo(path)
	if errRel != nil {
		return "", errRel
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
	if err := s.EnsureRepository(); err != nil {
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
	path, err := s.resolveDeletePath(id)
	if err != nil {
		return err
	}
	if err = s.EnsureRepository(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("auth filestore: delete failed: %w", err)
	}
	rel, errRel := s.relativeToRepo(path)
	if errRel != nil {
		return errRel
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
	if err := s.EnsureRepository(); err != nil {
		return err
	}

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

	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(message) == "" {
		message = "Sync watcher updates"
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
	if provider, ok := metadata["type"].(string); ok {
		return "", fmt.Errorf("panic")
	}
	return nil, nil
}
