package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/client"
	gitindex "github.com/go-git/go-git/v6/plumbing/format/index"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/go-git/go-git/v6/plumbing/transport/http"
	"github.com/go-git/go-git/v6/storage/filesystem/dotgit"

	runtimeconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

const (
	// gcInterval defines minimum time between garbage collection runs.
	gcInterval = 5 * time.Minute
	// gcPruneGracePeriod keeps recently orphaned objects available for recovery.
	gcPruneGracePeriod = 24 * time.Hour
)

// GitTokenStore persists token records and auth metadata using git as the backing storage.
type GitTokenStore struct {
	mu             sync.Mutex
	dirLock        sync.RWMutex
	baseDir        string
	repoDir        string
	configDir      string
	optionalConfig bool
	remote         string
	branch         string
	username       string
	password       string
	lastGC         time.Time
	syncBase       resolvedRemoteBranch
	pullFn         func(ctx context.Context, worktree *git.Worktree, pullOpts *git.PullOptions) error
	pushFn         func(ctx context.Context, repo *git.Repository, pushOpts *git.PushOptions) error
	refreshClaims  sync.Map
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
	return s.ensureRepositoryContextLocked(context.Background())
}

func (s *GitTokenStore) ensureRepositoryContextLocked(ctx context.Context) error {
	return s.syncRepositoryLocked(ctx, nil)
}

func (s *GitTokenStore) syncRepositoryLocked(ctx context.Context, seed *runtimeconfig.ConfigFileSnapshot) (errResult error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if errCtx := ctx.Err(); errCtx != nil {
		return errCtx
	}
	s.syncBase = resolvedRemoteBranch{}
	repoDir := s.repoDirSnapshot()
	if s.remote == "" || repoDir == "" {
		return fmt.Errorf("git token store: remote or repository path not configured")
	}
	if errMkdir := os.MkdirAll(repoDir, 0o700); errMkdir != nil {
		return errMkdir
	}
	if _, errRead := readGitConfigAt(repoDir); errRead != nil && !errors.Is(errRead, runtimeconfig.ErrConfigFileDirectory) {
		return errRead
	}
	gitDir := filepath.Join(repoDir, ".git")
	var repo *git.Repository
	var errOpen error
	if info, errStat := os.Lstat(gitDir); errors.Is(errStat, os.ErrNotExist) {
		repo, errOpen = git.PlainInit(repoDir, false)
		if errOpen == nil {
			_, errOpen = repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{s.remote}})
		}
	} else if errStat != nil || !info.IsDir() {
		return fmt.Errorf("git token store: expected a standalone .git directory: %v", errStat)
	} else {
		repo, errOpen = git.PlainOpen(repoDir)
	}
	defer func() {
		if repo != nil {
			errResult = errors.Join(errResult, repo.Close())
		}
	}()
	if errOpen != nil {
		return fmt.Errorf("git token store: open repository: %w", errOpen)
	}
	if errSigning := disableGitCommitSigning(repoDir); errSigning != nil {
		return errSigning
	}

	authMethod := s.gitClientOptions()
	current, localOID, errHead := gitHeadBranch(repo)
	if errHead != nil {
		return errHead
	}
	if errVerify := verifyRepositoryHead(repo); errVerify != nil {
		if !isRepositoryCorruptionError(errVerify) {
			return errVerify
		}
		errRecover := s.recoverRepositoryContextLocked(ctx, repoDir, authMethod, repo, nil, nil, (*git.Repository).Close, os.Rename)
		repo = nil
		if errRecover != nil {
			return errors.Join(errVerify, fmt.Errorf("git token store: recovery failed: %w", errRecover))
		}
		return s.syncRepositoryLocked(ctx, seed)
	}
	if s.pullFn != nil {
		worktree, errWorktree := repo.Worktree()
		if errWorktree != nil {
			return errWorktree
		}
		if errPull := s.pullFn(ctx, worktree, &git.PullOptions{ClientOptions: authMethod, RemoteName: "origin"}); errPull != nil && !errors.Is(errPull, git.NoErrAlreadyUpToDate) {
			return errPull
		}
	}
	// Select the branch and pin its advertised OID together. An independent
	// fetch can change tracking refs without changing this synchronization base.
	selected, errSelect := s.selectGitRemoteBranch(ctx, repo, current, authMethod)
	if errSelect != nil {
		if s.branch == "" && !localOID.IsZero() && errors.Is(errSelect, transport.ErrAuthenticationRequired) {
			return nil
		}
		return errSelect
	}
	target, remoteOID := selected.name, selected.hash
	if errValidate := target.Validate(); errValidate != nil {
		return errValidate
	}
	if remoteOID.IsZero() {
		if !localOID.IsZero() {
			return fmt.Errorf("git token store: remote branch %s not found", target)
		}
		if current != target {
			if errSwitch := updateGitWorktree(repo, repoDir, current, target, localOID, plumbing.ZeroHash, plumbing.ZeroHash, false, nil); errSwitch != nil {
				return errSwitch
			}
		}
		for _, path := range []string{"auths/.gitkeep", "config/.gitkeep"} {
			if errCreate := ensureGitPlaceholder(repoDir, path); errCreate != nil {
				return errCreate
			}
		}
		if errTracking := configureGitTracking(repo, target); errTracking != nil {
			return errTracking
		}
		s.syncBase = resolvedRemoteBranch{name: target}
		return s.commitAndPushWithOptionsLocked(ctx, "Initialize git token store", true, "auths/.gitkeep", "config/.gitkeep")
	}
	if errFetch := syncRemoteReferences(ctx, repo, authMethod); errFetch != nil {
		return errFetch
	}
	targetOID, errTarget := gitRefHash(repo, target)
	if errTarget != nil {
		return errTarget
	}
	if current != target {
		switchOID := targetOID
		if switchOID.IsZero() {
			switchOID = remoteOID
		}
		if errCheckout := updateGitWorktree(repo, repoDir, current, target, localOID, targetOID, switchOID, true, seed); errCheckout != nil {
			return errCheckout
		}
		localOID = switchOID
	}
	if localOID != remoteOID {
		advance, errAdvance := shouldAdvanceGitBranch(repo, target, localOID, remoteOID)
		if errAdvance != nil {
			return errAdvance
		}
		if advance {
			if errCheckout := updateGitWorktree(repo, repoDir, target, target, localOID, localOID, remoteOID, true, seed); errCheckout != nil {
				return errCheckout
			}
			localOID = remoteOID
		}
	}
	if localOID == remoteOID {
		if errRecord := recordGitSync(repo, target, remoteOID); errRecord != nil {
			return errRecord
		}
	}
	if errTracking := configureGitTracking(repo, target); errTracking != nil {
		return errTracking
	}
	s.syncBase = resolvedRemoteBranch{name: target, hash: remoteOID}
	if errMkdir := os.MkdirAll(s.baseDirSnapshot(), 0o700); errMkdir != nil {
		return errMkdir
	}
	// The config directory is created by rooted config/placeholder operations.
	return ensureGitConfigDir(repoDir)
}

func shouldAdvanceGitBranch(repo *git.Repository, branch plumbing.ReferenceName, local, remote plumbing.Hash) (bool, error) {
	if local.IsZero() {
		return true, nil
	}
	localCommit, errLocal := repo.CommitObject(local)
	if errLocal != nil {
		return false, errLocal
	}
	remoteCommit, errRemote := repo.CommitObject(remote)
	if errRemote != nil {
		return false, errRemote
	}
	fastForward, errFF := localCommit.IsAncestor(remoteCommit)
	if errFF != nil || fastForward {
		return fastForward, errFF
	}
	ahead, errAhead := remoteCommit.IsAncestor(localCommit)
	if errAhead != nil || ahead {
		return false, errAhead
	}
	// Store publications intentionally squash history. Only replace a forked
	// tip if it is the exact tip we previously synchronized, never a user commit.
	last, errLast := gitRefHash(repo, gitSyncedRef(branch))
	if errLast != nil {
		return false, errLast
	}
	if last != local {
		return false, fmt.Errorf("git token store: local and remote branch %s diverged: %w", branch, errGitLocalAdvanced)
	}
	return true, nil
}

// Save persists token storage and metadata to the resolved auth file path.
func (s *GitTokenStore) Save(ctx context.Context, auth *cliproxyauth.Auth) (string, error) {
	if auth == nil {
		return "", fmt.Errorf("auth filestore: auth is nil")
	}
	cliproxyauth.NormalizeCredentialMetadata(auth.Metadata)
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

	// Runtime updates must not recreate a disabled credential whose source file
	// was deliberately removed. Login and migration callers explicitly mark the
	// save when creating a missing disabled credential is intentional.
	if auth.Disabled && !cliproxyauth.HasAuthCreationIntent(ctx) {
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			return "", nil
		}
	}

	if err = s.ensureRepositoryContextLocked(ctx); err != nil {
		return "", err
	}
	relPath, errRel := s.relativeToRepo(path)
	if errRel != nil {
		return "", errRel
	}
	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("auth filestore: create dir failed: %w", err)
	}

	if handled, errCodex := cliproxyauth.PersistCodexAuthFile(ctx, path, auth); handled {
		if errCodex != nil {
			return "", errCodex
		}
	} else {
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
	if errCommit := s.commitAndPushContextLocked(ctx, fmt.Sprintf("Update auth %s", strings.TrimSpace(messageID)), relPath); errCommit != nil {
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

func (s *GitTokenStore) guardWatcherAuthRemovalLocked(message string, relPaths []string) (handled bool, errResult error) {
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
	defer func() {
		if errClose := repo.Close(); errClose != nil {
			errCloseWrap := fmt.Errorf("git token store: close repo for watcher removal guard: %w", errClose)
			if errResult == nil {
				errResult = errCloseWrap
			} else {
				errResult = errors.Join(errResult, errCloseWrap)
			}
		}
	}()
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
	cliproxyauth.NormalizeCredentialMetadata(metadata)
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

func disableGitCommitSigning(repoDir string) (errResult error) {
	repo, errOpen := git.PlainOpen(repoDir)
	if errOpen != nil {
		return fmt.Errorf("git token store: open repository config: %w", errOpen)
	}
	defer func() {
		if errClose := repo.Close(); errClose != nil {
			errCloseWrap := fmt.Errorf("git token store: close repository config: %w", errClose)
			if errResult == nil {
				errResult = errCloseWrap
			} else {
				errResult = errors.Join(errResult, errCloseWrap)
			}
		}
	}()
	cfg, errConfig := repo.Config()
	if errConfig != nil {
		return fmt.Errorf("git token store: get repository config: %w", errConfig)
	}
	if cfg.Commit.GpgSign == config.OptBoolFalse {
		return nil
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

func syncRemoteReferences(ctx context.Context, repo *git.Repository, authMethod []client.Option) error {
	if err := repo.FetchContext(ctx, &git.FetchOptions{ClientOptions: authMethod, RemoteName: "origin"}); err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return err
	}
	return nil
}

// selectGitRemoteBranch returns the advertised tip, not a mutable tracking ref.
func (s *GitTokenStore) selectGitRemoteBranch(ctx context.Context, repo *git.Repository, current plumbing.ReferenceName, authMethod []client.Option) (resolvedRemoteBranch, error) {
	remote, errRemote := repo.Remote("origin")
	if errRemote != nil {
		return resolvedRemoteBranch{}, errRemote
	}
	refs, errList := remote.ListContext(ctx, &git.ListOptions{ClientOptions: authMethod})
	if errList != nil && !errors.Is(errList, transport.ErrEmptyRemoteRepository) {
		return resolvedRemoteBranch{}, errList
	}
	var advertised, first resolvedRemoteBranch
	var defaultBranch plumbing.ReferenceName
	var defaultOID plumbing.Hash
	for _, ref := range refs {
		if ref.Name() == plumbing.HEAD {
			defaultBranch, _ = normalizeRemoteBranchReference(ref.Target())
			defaultOID = ref.Hash()
			break
		}
	}
	if defaultBranch == "" {
		if ref, errRef := repo.Reference(plumbing.NewRemoteReferenceName("origin", "HEAD"), false); errRef == nil && ref.Type() == plumbing.SymbolicReference {
			defaultBranch, _ = normalizeRemoteBranchReference(ref.Target())
		}
	}
	wanted := defaultBranch
	if s.branch != "" {
		wanted = plumbing.NewBranchReferenceName(s.branch)
	}
	for _, ref := range refs {
		if !ref.Name().IsBranch() || ref.Type() != plumbing.HashReference {
			continue
		}
		resolved := resolvedRemoteBranch{name: ref.Name(), hash: ref.Hash()}
		if ref.Name() == wanted {
			return resolved, nil
		}
		if first.name == "" {
			first = resolved
		}
		if !defaultOID.IsZero() && ref.Hash() == defaultOID {
			advertised = resolved
		}
	}
	if s.branch != "" {
		if len(refs) != 0 && first.name != "" {
			return resolvedRemoteBranch{}, fmt.Errorf("git token store: remote branch %s not found", wanted)
		}
		return resolvedRemoteBranch{name: wanted}, nil
	}
	if advertised.name != "" {
		return advertised, nil
	}
	if first.name != "" {
		return first, nil
	}
	return resolvedRemoteBranch{name: current}, nil
}

func configureGitTracking(repo *git.Repository, branch plumbing.ReferenceName) error {
	cfg, errConfig := repo.Config()
	if errConfig != nil {
		return errConfig
	}
	name := branch.Short()
	if cfg.Branches[name] != nil {
		return nil
	}
	cfg.Branches[name] = &config.Branch{Name: name, Remote: "origin", Merge: branch}
	return repo.SetConfig(cfg)
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
		if pathsOverlap(path, dirtyPath) {
			return dirtyPath, true
		}
	}
	return "", false
}

func pathsOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, b+"/") || strings.HasPrefix(b, a+"/")
}

func closeRecoveryRepositories(closeRepository func(*git.Repository) error, baselineRepo, clonedRepo *git.Repository) error {
	var errClose error
	if baselineRepo != nil {
		if err := closeRepository(baselineRepo); err != nil {
			errClose = errors.Join(errClose, fmt.Errorf("close recovery baseline repository: %w", err))
		}
	}
	if clonedRepo != nil {
		if err := closeRepository(clonedRepo); err != nil {
			errClose = errors.Join(errClose, fmt.Errorf("close cloned recovery repository: %w", err))
		}
	}
	return errClose
}

func (s *GitTokenStore) recoverRepositoryLocked(repoDir string, authMethod []client.Option, callerRepo *git.Repository, baselineTree *object.Tree, dirtyPaths map[string]struct{}, closeRepository func(*git.Repository) error, renameWorktreeEntry func(string, string) error) error {
	return s.recoverRepositoryContextLocked(context.Background(), repoDir, authMethod, callerRepo, baselineTree, dirtyPaths, closeRepository, renameWorktreeEntry)
}

func (s *GitTokenStore) recoverRepositoryContextLocked(ctx context.Context, repoDir string, authMethod []client.Option, callerRepo *git.Repository, baselineTree *object.Tree, dirtyPaths map[string]struct{}, closeRepository func(*git.Repository) error, renameWorktreeEntry func(string, string) error) (errRecovery error) {
	parentDir := filepath.Dir(repoDir)
	recoveryRoot, errTemp := os.MkdirTemp(parentDir, ".gitstore-recovery-")
	if errTemp != nil {
		if callerRepo != nil {
			if errClose := closeRepository(callerRepo); errClose != nil {
				return errors.Join(
					fmt.Errorf("create recovery directory: %w", errTemp),
					fmt.Errorf("close recovery caller repository: %w", errClose),
				)
			}
		}
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

	var baselineRepo *git.Repository
	if baselineTree == nil {
		if callerRepo != nil {
			if errClose := closeRepository(callerRepo); errClose != nil {
				return fmt.Errorf("close recovery caller repository before baseline inspection: %w", errClose)
			}
		}
		inspectedRepo, inspectedTree, inspectedDirtyPaths, errInspect := inspectRecoveryBaseline(repoDir)
		if errInspect != nil {
			return fmt.Errorf("inspect recovery baseline: %w", errInspect)
		}
		baselineRepo = inspectedRepo
		baselineTree = inspectedTree
		dirtyPaths = inspectedDirtyPaths
	} else {
		baselineRepo = callerRepo
	}

	cloneDir := filepath.Join(recoveryRoot, "clone")
	cloneOpts := &git.CloneOptions{ClientOptions: authMethod, URL: s.remote}
	if s.branch != "" {
		cloneOpts.ReferenceName = plumbing.NewBranchReferenceName(s.branch)
	}
	clonedRepo, errClone := git.PlainCloneContext(ctx, cloneDir, cloneOpts)
	if errClone != nil {
		if baselineRepo != nil {
			if errClose := closeRepository(baselineRepo); errClose != nil {
				return errors.Join(
					fmt.Errorf("clone remote repository: %w", errClone),
					fmt.Errorf("close recovery baseline repository: %w", errClose),
				)
			}
		}
		return fmt.Errorf("clone remote repository: %w", errClone)
	}
	defer func() {
		if errClose := closeRecoveryRepositories(closeRepository, baselineRepo, clonedRepo); errClose != nil {
			errRecovery = errors.Join(errRecovery, errClose)
		}
	}()
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
	// Leave the original config directory in place. A writer may edit or
	// create config after the clone, so copying or replacing it loses updates.
	changedPaths, errChanged := changedTreePaths(baselineTree, remoteTree)
	if errChanged != nil {
		return errChanged
	}
	for _, path := range changedPaths {
		if pathsOverlap(path, "config") {
			return fmt.Errorf("remote path %s changes the local config directory during recovery: %w", path, runtimeconfig.ErrConfigFileChanged)
		}
	}
	if _, errBlob := gitConfigBlob(remoteTree); errBlob != nil {
		return errBlob
	}
	if errRecord := recordGitSync(clonedRepo, clonedHead.Name(), clonedHead.Hash()); errRecord != nil {
		return errRecord
	}
	preservedPaths, errPreserve := recoveryPreservedPaths(baselineTree, remoteTree, dirtyPaths)
	if errPreserve != nil {
		return errPreserve
	}
	if errApply := applyRecoveryLocalChanges(repoDir, cloneDir, preservedPaths); errApply != nil {
		return fmt.Errorf("preserve local worktree changes: %w", errApply)
	}

	errClose := closeRecoveryRepositories(closeRepository, baselineRepo, clonedRepo)
	baselineRepo = nil
	clonedRepo = nil
	if errClose != nil {
		return errClose
	}

	backupWorktreeDir := filepath.Join(recoveryRoot, "worktree")
	retainWorktreeBackup, errBackup := moveWorktreeEntries(repoDir, backupWorktreeDir, renameWorktreeEntry)
	if errBackup != nil {
		if retainWorktreeBackup {
			cleanupRecovery = false
			return fmt.Errorf("backup existing worktree; backup retained at %s: %w", backupWorktreeDir, errBackup)
		}
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
		if _, errRestore := moveWorktreeEntries(backupWorktreeDir, repoDir, renameWorktreeEntry); errRestore != nil {
			cleanupRecovery = false
			return errors.Join(errInstall, fmt.Errorf("restore worktree; backup retained at %s: %w", backupWorktreeDir, errRestore))
		}
		return errInstall
	}
	if _, errMove := moveWorktreeEntries(cloneDir, repoDir, renameWorktreeEntry); errMove != nil {
		errMoveWorktree := fmt.Errorf("install recovered worktree: %w", errMove)
		if errRollback := rollbackRecoveredRepository(repoDir, gitDir, backupGitDir, backupWorktreeDir, renameWorktreeEntry); errRollback != nil {
			cleanupRecovery = false
			return errors.Join(errMoveWorktree, fmt.Errorf("rollback recovered repository; backup retained at %s: %w", recoveryRoot, errRollback))
		}
		return errMoveWorktree
	}
	recoveredRepo, errOpen := git.PlainOpen(repoDir)
	if errOpen == nil {
		errVerify := verifyRepositoryHead(recoveredRepo)
		errClose := closeRepository(recoveredRepo)
		if errVerify != nil && errClose != nil {
			errOpen = errors.Join(errVerify, fmt.Errorf("close recovered repository: %w", errClose))
		} else if errVerify != nil {
			errOpen = errVerify
		} else if errClose != nil {
			errOpen = fmt.Errorf("close recovered repository: %w", errClose)
		}
	}
	if errOpen != nil {
		errRecovered := fmt.Errorf("verify recovered repository: %w", errOpen)
		if errRollback := rollbackRecoveredRepository(repoDir, gitDir, backupGitDir, backupWorktreeDir, renameWorktreeEntry); errRollback != nil {
			cleanupRecovery = false
			return errors.Join(errRecovered, fmt.Errorf("rollback recovered repository; backup retained at %s: %w", recoveryRoot, errRollback))
		}
		return errRecovered
	}
	return nil
}

func inspectRecoveryBaseline(repoDir string) (repoResult *git.Repository, treeResult *object.Tree, dirtyResult map[string]struct{}, errResult error) {
	openedRepo, errOpen := git.PlainOpen(repoDir)
	if errOpen != nil {
		return nil, nil, nil, fmt.Errorf("open repository: %w", errOpen)
	}
	defer func() {
		if errResult != nil && openedRepo != nil {
			if errClose := openedRepo.Close(); errClose != nil {
				errResult = errors.Join(errResult, fmt.Errorf("close baseline repository on inspection failure: %w", errClose))
			}
			openedRepo = nil
		}
	}()
	head, errHead := openedRepo.Head()
	if errHead != nil {
		return nil, nil, nil, fmt.Errorf("inspect head: %w", errHead)
	}
	commit, errCommit := openedRepo.CommitObject(head.Hash())
	if errCommit != nil {
		return nil, nil, nil, fmt.Errorf("inspect head commit: %w", errCommit)
	}
	tree, errTree := commit.Tree()
	if errTree != nil {
		return nil, nil, nil, fmt.Errorf("inspect head tree: %w", errTree)
	}
	local, errRead := readGitConfigAt(repoDir)
	if errRead != nil {
		return nil, nil, nil, errRead
	}
	dirtyPaths, errDirty := worktreeDirtyPaths(openedRepo, local)
	if errDirty != nil {
		return nil, nil, nil, fmt.Errorf("inspect worktree changes: %w", errDirty)
	}
	liveIndex, errIndex := openedRepo.Storer.Index()
	if errIndex != nil {
		return nil, nil, nil, errIndex
	}
	baseIndex, errBase := indexFromGitTree(tree)
	if errBase != nil {
		return nil, nil, nil, errBase
	}
	basePaths, livePaths := indexedPaths(baseIndex), indexedPaths(liveIndex)
	for path := range dirtyPaths {
		if !sameGitEntries(basePaths[path], livePaths[path]) {
			return nil, nil, nil, fmt.Errorf("staged path %s cannot be recovered safely", path)
		}
	}
	last, errLast := gitRefHash(openedRepo, gitSyncedRef(head.Name()))
	if errLast != nil || last != head.Hash() {
		return nil, nil, nil, errors.Join(errGitLocalAdvanced, errLast)
	}
	return openedRepo, tree, dirtyPaths, nil
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
		if pathsOverlap(path, "config") {
			continue
		}
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

func moveWorktreeEntries(sourceDir, targetDir string, rename func(string, string) error) (bool, error) {
	if rename == nil {
		return false, fmt.Errorf("rename function is nil")
	}
	if errMkdir := os.MkdirAll(targetDir, 0o700); errMkdir != nil {
		return false, errMkdir
	}
	entries, errRead := os.ReadDir(sourceDir)
	if errRead != nil {
		return false, errRead
	}
	moved := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.Name() == ".git" || strings.EqualFold(entry.Name(), "config") {
			continue
		}
		source := filepath.Join(sourceDir, entry.Name())
		target := filepath.Join(targetDir, entry.Name())
		if errRename := rename(source, target); errRename != nil {
			errMove := fmt.Errorf("move %s: %w", entry.Name(), errRename)
			retainTarget := false
			for index := len(moved) - 1; index >= 0; index-- {
				name := moved[index]
				if errRestore := rename(filepath.Join(targetDir, name), filepath.Join(sourceDir, name)); errRestore != nil {
					retainTarget = true
					errMove = errors.Join(errMove, fmt.Errorf("restore %s: %w", name, errRestore))
				}
			}
			return retainTarget, errMove
		}
		moved = append(moved, entry.Name())
	}
	return false, nil
}

func removeWorktreeEntries(repoDir string) error {
	entries, errRead := os.ReadDir(repoDir)
	if errRead != nil {
		return errRead
	}
	for _, entry := range entries {
		if entry.Name() == ".git" || strings.EqualFold(entry.Name(), "config") {
			continue
		}
		if errRemove := os.RemoveAll(filepath.Join(repoDir, entry.Name())); errRemove != nil {
			return errRemove
		}
	}
	return nil
}

func rollbackRecoveredRepository(repoDir, gitDir, backupGitDir, backupWorktreeDir string, renameWorktreeEntry func(string, string) error) error {
	if errRemove := removeWorktreeEntries(repoDir); errRemove != nil {
		return fmt.Errorf("remove recovered worktree: %w", errRemove)
	}
	if errRollback := rollbackRecoveredGitDirectory(gitDir, backupGitDir); errRollback != nil {
		return errRollback
	}
	if _, errRestore := moveWorktreeEntries(backupWorktreeDir, repoDir, renameWorktreeEntry); errRestore != nil {
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
	if errors.Is(err, dotgit.ErrPackfileNotFound) ||
		errors.Is(err, dotgit.ErrIdxNotFound) ||
		errors.Is(err, plumbing.ErrObjectNotFound) {
		return true
	}
	return errors.Is(err, fs.ErrNotExist) && err != nil && strings.Contains(err.Error(), "packhandle:")
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
	if _, errConfig := gitConfigBlob(tree); errConfig != nil {
		return errConfig
	}
	files := tree.Files()
	return files.ForEach(func(file *object.File) error {
		_, errContents := file.Contents()
		return errContents
	})
}

func (s *GitTokenStore) commitAndPushLocked(message string, relPaths ...string) error {
	return s.commitAndPushWithOptionsLocked(context.Background(), message, false, relPaths...)
}

func (s *GitTokenStore) commitAndPushContextLocked(ctx context.Context, message string, relPaths ...string) error {
	return s.commitAndPushWithOptionsLocked(ctx, message, false, relPaths...)
}

func (s *GitTokenStore) commitAndPushInitialLocked(message string, relPaths ...string) error {
	return s.commitAndPushWithOptionsLocked(context.Background(), message, true, relPaths...)
}

func (s *GitTokenStore) commitAndPushWithOptionsLocked(ctx context.Context, message string, allowMissingRemote bool, relPaths ...string) error {
	return s.commitAndPushSnapshotLocked(ctx, message, allowMissingRemote, nil, relPaths...)
}

func (s *GitTokenStore) commitAndPushSnapshotLocked(ctx context.Context, message string, allowMissingRemote bool, upload *gitConfigUpload, relPaths ...string) (errResult error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if errCtx := ctx.Err(); errCtx != nil {
		return errCtx
	}
	repoDir := s.repoDirSnapshot()
	if repoDir == "" {
		return fmt.Errorf("git token store: repository path not configured")
	}
	repo, err := git.PlainOpen(repoDir)
	if err != nil {
		return fmt.Errorf("git token store: open repo: %w", err)
	}
	defer func() {
		if errClose := repo.Close(); errClose != nil {
			errCloseWrap := fmt.Errorf("git token store: close repo: %w", errClose)
			if errResult == nil {
				errResult = errCloseWrap
			} else {
				errResult = errors.Join(errResult, errCloseWrap)
			}
		}
	}()
	managedPaths, errPaths := normalizeManagedPaths(relPaths)
	if errPaths != nil {
		return fmt.Errorf("git token store: validate commit paths: %w", errPaths)
	}
	if len(managedPaths) == 0 {
		return nil
	}
	branch, base, errHead := gitHeadBranch(repo)
	if errHead != nil {
		return errHead
	}
	expected := s.syncBase
	if expected.name == "" && allowMissingRemote && base.IsZero() {
		expected.name = branch
	}
	if expected.name != branch || expected.hash != base || (base.IsZero() && !allowMissingRemote) {
		return fmt.Errorf("git token store: refusing to publish an unsynchronized local branch: %w", errGitLocalAdvanced)
	}
	root, errRoot := os.OpenRoot(repoDir)
	if errRoot != nil {
		return errRoot
	}
	defer func() { errResult = errors.Join(errResult, root.Close()) }()
	local, errLocal := readGitConfigSnapshot(root)
	if errLocal != nil && !errors.Is(errLocal, runtimeconfig.ErrConfigFileDirectory) {
		return errLocal
	}
	tree, errTree := gitTreeAt(repo, base)
	if errTree != nil {
		return errTree
	}
	if _, errConfig := gitConfigBlob(tree); errConfig != nil {
		return errConfig
	}
	idx, errIndex := indexFromGitTree(tree)
	if errIndex != nil {
		return errIndex
	}
	originalWorktree, errWorktree := repo.Worktree()
	if errWorktree != nil {
		return errWorktree
	}
	snapshotFS, errSnapshot := gitSnapshotFS(originalWorktree.Filesystem(), local)
	if errSnapshot != nil {
		return errSnapshot
	}
	scratch := &gitCandidateStorage{Storer: repo.Storer, index: idx, refs: map[plumbing.ReferenceName]*plumbing.Reference{
		plumbing.HEAD: plumbing.NewSymbolicReference(plumbing.HEAD, branch),
	}}
	if !base.IsZero() {
		scratch.refs[branch] = plumbing.NewHashReference(branch, base)
	}
	candidateRepo, errOpen := git.Open(scratch, snapshotFS)
	if errOpen != nil {
		return errOpen
	}
	worktree, errWorktree := candidateRepo.Worktree()
	if errWorktree != nil {
		return errWorktree
	}
	for _, path := range managedPaths {
		switch {
		case path == gitConfigPath:
			if errLocal != nil {
				return errLocal
			}
			var data []byte
			if upload != nil {
				data = upload.data
			} else if local != nil {
				data = local.Bytes()
			}
			if _, errStage := stageGitConfig(candidateRepo, data); errStage != nil {
				return errStage
			}
		case pathsOverlap(path, gitConfigPath):
			return fmt.Errorf("git token store: cannot stage config through path %s", path)
		default:
			name := filepath.FromSlash(path)
			if errParent := gitPathParents(root, name, false); errParent != nil && !errors.Is(errParent, os.ErrNotExist) {
				return errParent
			}
			if errAdd := runtimeconfig.WithConfigFileOpenGate(func() error { _, err := worktree.Add(path); return err }); errAdd != nil && !errors.Is(errAdd, gitindex.ErrEntryNotFound) && !errors.Is(errAdd, os.ErrNotExist) {
				return fmt.Errorf("git token store: add %s: %w", path, errAdd)
			}
		}
	}
	if strings.TrimSpace(message) == "" {
		message = "Update auth store"
	}
	signature := &object.Signature{Name: "CLIProxyAPI", Email: "cliproxy@local", When: time.Now()}
	commitOID, errCommit := worktree.Commit(message, &git.CommitOptions{Author: signature, Committer: signature})
	if errors.Is(errCommit, git.ErrEmptyCommit) {
		if upload == nil {
			return nil
		}
		actual, errRemote := s.refreshRemoteRef(ctx, repo, branch)
		if errRemote != nil {
			return errRemote
		}
		if actual != expected.hash {
			return errGitRemoteAdvanced
		}
		upload.hash, errCommit = verifyGitConfigBlob(repo, base, upload)
		return errCommit
	}
	if errCommit != nil {
		return fmt.Errorf("git token store: build commit: %w", errCommit)
	}
	if !base.IsZero() {
		if errValidate := validateManagedTreeChanges(repo, base, commitOID, managedPaths); errValidate != nil {
			return errValidate
		}
	}
	candidate, errCandidate := makeGitSnapshotCommit(repo, plumbing.NewHashReference(branch, commitOID), message, signature)
	if errCandidate != nil {
		return errCandidate
	}
	// A concurrent user commit cannot change the immutable upload or be rolled
	// back when the push fails, since the candidate never replaced local HEAD.
	current, oid, errCurrent := gitHeadBranch(repo)
	if errCurrent != nil || current != branch || oid != base {
		return errors.Join(errGitLocalAdvanced, errCurrent)
	}
	hash, errPush := s.pushRepositoryLocked(ctx, repo, candidate, expected.hash, upload)
	if errPush != nil {
		current, oid, errCurrent = gitHeadBranch(repo)
		if errCurrent != nil || current != branch || oid != base {
			return errors.Join(errGitLocalAdvanced, errPush, errCurrent)
		}
		return errPush
	}
	if upload != nil {
		upload.hash = hash
	}
	if errUpdate := updateGitWorktree(repo, repoDir, branch, branch, base, base, candidate.Hash(), false, nil); errUpdate != nil {
		// The push succeeded. Leave any concurrently committed branch alone; the
		// next sync recognizes it as user-owned, not the previous replica tip.
		if errors.Is(errUpdate, errGitLocalAdvanced) {
			return nil
		}
		return errUpdate
	}
	if errRecord := recordGitSync(repo, branch, candidate.Hash()); errRecord != nil {
		return errRecord
	}
	s.syncBase = resolvedRemoteBranch{name: branch, hash: candidate.Hash()}
	s.maybeRunGC(repoDir)
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

func (s *GitTokenStore) pushRepositoryLocked(ctx context.Context, repo *git.Repository, headRef *plumbing.Reference, expected plumbing.Hash, upload *gitConfigUpload) (string, error) {
	branchName := headRef.Name()
	pushOpts := &git.PushOptions{
		ClientOptions: s.gitClientOptions(),
		RefSpecs:      []config.RefSpec{config.RefSpec(headRef.Hash().String() + ":" + branchName.String())},
	}
	if !expected.IsZero() {
		pushOpts.RequireRemoteRefs = []config.RefSpec{config.RefSpec(expected.String() + ":" + branchName.String())}
		pushOpts.RefSpecs[0] = "+" + pushOpts.RefSpecs[0]
	}
	// Validate the immutable blob of the actual candidate, including when an
	// auth-only push inherits a config or a caller stages it via another API.
	hash, errVerify := verifyGitConfigBlob(repo, headRef.Hash(), upload)
	if errVerify != nil {
		return "", errVerify
	}
	var errPush error
	if s.pushFn != nil {
		errPush = s.pushFn(ctx, repo, pushOpts)
	} else {
		errPush = s.withGitRemoteFileLock(func() error { return repo.PushContext(ctx, pushOpts) })
	}
	if errPush != nil {
		if !errors.Is(errPush, git.NoErrAlreadyUpToDate) {
			if actual, errList := s.refreshRemoteRef(ctx, repo, branchName); errList == nil && actual != expected {
				return "", errors.Join(errGitRemoteAdvanced, fmt.Errorf("git token store: push: %w", errPush))
			}
			return "", fmt.Errorf("git token store: push: %w", errPush)
		}
	}
	return hash, nil
}

// makeGitSnapshotCommit returns a parentless commit without modifying a reference.
func makeGitSnapshotCommit(repo *git.Repository, before *plumbing.Reference, message string, signature *object.Signature) (*plumbing.Reference, error) {
	commitObj, err := repo.CommitObject(before.Hash())
	if err != nil {
		return nil, fmt.Errorf("git token store: inspect head commit: %w", err)
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
		return nil, fmt.Errorf("git token store: encode squashed commit: %w", err)
	}
	newHash, err := repo.Storer.SetEncodedObject(mem)
	if err != nil {
		return nil, fmt.Errorf("git token store: write squashed commit: %w", err)
	}
	return plumbing.NewHashReference(before.Name(), newHash), nil
}

func (s *GitTokenStore) maybeRunGC(repoDir string) {
	now := time.Now()
	if now.Sub(s.lastGC) < gcInterval {
		return
	}
	s.lastGC = now

	repo, err := git.PlainOpen(repoDir)
	if err != nil {
		log.Warnf("git token store: open repository for GC: %v", err)
		return
	}
	defer func() {
		if errClose := repo.Close(); errClose != nil {
			log.Warnf("git token store: close repository after GC: %v", errClose)
		}
	}()

	pruneOpts := git.PruneOptions{
		OnlyObjectsOlderThan: now.Add(-gcPruneGracePeriod),
		Handler:              repo.DeleteObject,
	}
	if err := repo.Prune(pruneOpts); err != nil && !errors.Is(err, git.ErrLooseObjectsNotSupported) {
		return
	}
	_ = repo.RepackObjects(&git.RepackConfig{})
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
