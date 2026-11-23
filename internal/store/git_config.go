package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	gitindex "github.com/go-git/go-git/v6/plumbing/format/index"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

const gitConfigPath = "config/config.yaml"

var (
	errGitRemoteAdvanced = errors.New("git token store: remote branch advanced")
	errGitLocalAdvanced  = errors.New("git token store: local branch advanced during persistence")
)

type gitConfigUpload struct {
	data []byte
	hash string
}

// SetOptionalConfig enables standby behavior when the config cannot be decoded
// or does not exist. Call before using the store.
func (s *GitTokenStore) SetOptionalConfig(optional bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.optionalConfig = optional
}

// BootstrapConfig synchronizes the repository before preparing any automatic
// rewrites. A losing candidate never touches the worktree; synchronize again to
// get the winner instead of restoring over any user edits.
func (s *GitTokenStore) BootstrapConfig(ctx context.Context, example string) (*config.Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Capture the seed once. A config created, replaced, or edited while booting
	// is not ours to overwrite, even if this replica must re-sync after a race.
	seed, errSeed := readGitConfigAt(s.repoDirSnapshot())
	if errSeed != nil && !(s.optionalConfig && errors.Is(errSeed, config.ErrConfigFileDirectory)) {
		return nil, errSeed
	}
	for range configSyncAttempts {
		if errSync := s.syncRepositoryLocked(ctx, seed); errSync != nil {
			if errors.Is(errSync, errGitRemoteAdvanced) && !errors.Is(errSync, errGitLocalAdvanced) {
				continue
			}
			return nil, errSync
		}
		local, errRead := readGitConfigAt(s.repoDirSnapshot())
		if errRead != nil {
			if s.optionalConfig && errors.Is(errRead, config.ErrConfigFileDirectory) {
				return config.ParsePreparedConfigBytes(nil)
			}
			return nil, errRead
		}
		if local == nil {
			if errSeed := s.checkGitConfigSeed(); errSeed != nil {
				return nil, errSeed
			}
		}
		data, errRead := localConfigOrTemplate(local, example, s.optionalConfig)
		if errRead != nil {
			return nil, errRead
		}
		prepared, errPrepare := config.PrepareConfigBytesForUploadWithOptional(data, s.optionalConfig)
		if errPrepare != nil {
			return nil, errPrepare
		}
		if prepared == nil {
			return config.ParsePreparedConfigBytes(nil)
		}
		hash, errPersist := s.persistPreparedConfigLocked(ctx, prepared)
		if errors.Is(errPersist, errGitRemoteAdvanced) && !errors.Is(errPersist, errGitLocalAdvanced) {
			continue
		}
		if errPersist != nil {
			return nil, errPersist
		}
		cfg, errParse := config.ParsePreparedConfigBytes(prepared)
		if errParse != nil {
			return nil, errParse
		}
		if cfg.SourceHash != hash {
			return nil, fmt.Errorf("git token store: local config does not match the remote config snapshot: %w", config.ErrConfigFileChanged)
		}
		if errWrite := withGitRoot(s.repoDirSnapshot(), func(root *os.Root) error { return applyGitConfigSnapshot(root, local, prepared) }); errWrite != nil {
			return nil, errWrite
		}
		return cfg, nil
	}
	return nil, fmt.Errorf("git token store: config changed during %d synchronization attempts", configSyncAttempts)
}

// checkGitConfigSeed uses the advertised OID captured by synchronization, not
// the local branch or a tracking ref that another fetch could have moved.
func (s *GitTokenStore) checkGitConfigSeed() (errResult error) {
	if s.syncBase.name == "" || s.syncBase.hash.IsZero() {
		return fmt.Errorf("git token store: cannot seed config without a synchronized remote branch")
	}
	repo, errOpen := git.PlainOpen(s.repoDirSnapshot())
	if errOpen != nil {
		return errOpen
	}
	defer func() { errResult = errors.Join(errResult, repo.Close()) }()
	tree, errTree := gitTreeAt(repo, s.syncBase.hash)
	if errTree != nil {
		return errTree
	}
	data, errRead := gitConfigBlob(tree)
	if errRead != nil {
		return errRead
	}
	if data != nil {
		return fmt.Errorf("git token store: remote config exists but local config is missing: %w", config.ErrConfigFileChanged)
	}
	return nil
}

// PersistConfig commits and pushes a verified snapshot of the local config.
func (s *GitTokenStore) PersistConfig(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if errSync := s.ensureRepositoryContextLocked(ctx); errSync != nil {
		return errSync
	}
	return withGitRoot(s.repoDirSnapshot(), func(root *os.Root) error {
		local, errLocal := readGitConfigSnapshot(root)
		if errLocal != nil {
			if s.optionalConfig && errors.Is(errLocal, config.ErrConfigFileDirectory) {
				return nil
			}
			return errLocal
		}
		if local == nil {
			if s.optionalConfig {
				return nil
			}
			return os.ErrNotExist
		}
		prepared, errPrepare := config.PrepareConfigBytesForUploadWithOptional(local.Bytes(), s.optionalConfig)
		if errPrepare != nil || prepared == nil {
			return errPrepare
		}
		if _, errPersist := s.persistPreparedConfigLocked(ctx, prepared); errPersist != nil {
			return errPersist
		}
		return applyGitConfigSnapshot(root, local, prepared)
	})
}

// PersistPreparedConfig publishes the provided bytes, not the live worktree
// file. The returned SHA-256 is computed from the validated commit's actual blob.
// A nil snapshot denotes standby and does not publish anything.
func (s *GitTokenStore) PersistPreparedConfig(ctx context.Context, prepared []byte) (string, error) {
	if prepared == nil {
		return "", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if errSync := s.ensureRepositoryContextLocked(ctx); errSync != nil {
		return "", errSync
	}
	return s.persistPreparedConfigLocked(ctx, prepared)
}

func (s *GitTokenStore) persistPreparedConfigLocked(ctx context.Context, prepared []byte) (string, error) {
	upload := &gitConfigUpload{data: prepared}
	if errPush := s.commitAndPushSnapshotLocked(ctx, "Update config", false, upload, gitConfigPath); errPush != nil {
		return "", errPush
	}
	return upload.hash, nil
}

func stageGitConfig(repo *git.Repository, data []byte) (bool, error) {
	index, errIndex := repo.Storer.Index()
	if errIndex != nil {
		return false, errIndex
	}
	if data == nil {
		if _, errRemove := index.Remove(gitConfigPath); errRemove != nil {
			if errors.Is(errRemove, gitindex.ErrEntryNotFound) {
				return false, nil
			}
			return false, errRemove
		}
		return true, repo.Storer.SetIndex(index)
	}
	blob := repo.Storer.NewEncodedObject()
	blob.SetType(plumbing.BlobObject)
	blob.SetSize(int64(len(data)))
	writer, errWriter := blob.Writer()
	if errWriter != nil {
		return false, errWriter
	}
	_, errWrite := writer.Write(data)
	if errBlob := errors.Join(errWrite, writer.Close()); errBlob != nil {
		return false, errBlob
	}
	hash, errStore := repo.Storer.SetEncodedObject(blob)
	if errStore != nil {
		return false, errStore
	}
	entry, errEntry := index.Entry(gitConfigPath)
	if errors.Is(errEntry, gitindex.ErrEntryNotFound) {
		entry, errEntry = index.Add(gitConfigPath)
	}
	if errEntry != nil {
		return false, errEntry
	}
	*entry = gitindex.Entry{Name: gitConfigPath, Hash: hash, Mode: filemode.Regular, Size: uint32(len(data))}
	return true, repo.Storer.SetIndex(index)
}

func gitConfigBlob(tree *object.Tree) ([]byte, error) {
	parent, errParent := tree.FindEntry("config")
	if errors.Is(errParent, object.ErrEntryNotFound) {
		return nil, nil
	}
	if errParent != nil {
		return nil, errParent
	}
	if parent.Mode != filemode.Dir {
		return nil, fmt.Errorf("git config parent must be a tree, not a link")
	}
	entry, errEntry := tree.FindEntry(gitConfigPath)
	if errors.Is(errEntry, object.ErrEntryNotFound) {
		return nil, nil
	}
	if errEntry != nil {
		return nil, errEntry
	}
	if entry.Mode != filemode.Regular && entry.Mode != filemode.Executable && entry.Mode != filemode.Deprecated {
		return nil, fmt.Errorf("git config must be a regular file")
	}
	file, errFile := tree.File(gitConfigPath)
	if errFile != nil {
		return nil, errFile
	}
	data, errRead := file.Contents()
	return []byte(data), errRead
}

func verifyGitConfigBlob(repo *git.Repository, commitHash plumbing.Hash, upload *gitConfigUpload) (string, error) {
	commit, errCommit := repo.CommitObject(commitHash)
	if errCommit != nil {
		return "", errCommit
	}
	tree, errTree := commit.Tree()
	if errTree != nil {
		return "", errTree
	}
	data, errRead := gitConfigBlob(tree)
	if errRead != nil {
		return "", errRead
	}
	if upload != nil && (data == nil || !bytes.Equal(data, upload.data)) {
		return "", fmt.Errorf("git config blob differs from the prepared snapshot")
	}
	if data == nil {
		return "", nil
	}
	if errVerify := config.VerifyConfigBytesForUpload(data); errVerify != nil {
		return "", fmt.Errorf("verify git config blob before push: %w", errVerify)
	}
	return config.ComputeSourceHash(data), nil
}

func withGitRoot(repoDir string, use func(*os.Root) error) (errResult error) {
	root, errOpen := os.OpenRoot(repoDir)
	if errOpen != nil {
		return errOpen
	}
	defer func() { errResult = errors.Join(errResult, root.Close()) }()
	return use(root)
}

func readGitConfigSnapshot(root *os.Root) (*config.ConfigFileSnapshot, error) {
	snapshot, errRead := config.ReadRootedConfigFileSnapshot(root, filepath.FromSlash(gitConfigPath))
	if errors.Is(errRead, os.ErrNotExist) && !errors.Is(errRead, config.ErrConfigFileChanged) {
		return nil, nil
	}
	return snapshot, errRead
}

func readGitConfigAt(repoDir string) (snapshot *config.ConfigFileSnapshot, errResult error) {
	if repoDir == "" {
		return nil, fmt.Errorf("git token store: repository path not configured")
	}
	errResult = withGitRoot(repoDir, func(root *os.Root) error {
		var errRead error
		snapshot, errRead = readGitConfigSnapshot(root)
		return errRead
	})
	if errors.Is(errResult, os.ErrNotExist) && !errors.Is(errResult, config.ErrConfigFileChanged) {
		return nil, nil
	}
	return snapshot, errResult
}

func applyGitConfigSnapshot(root *os.Root, local *config.ConfigFileSnapshot, data []byte) error {
	return config.ApplyRootedConfigFileCAS(root, filepath.FromSlash(gitConfigPath), local, data)
}

func ensureGitPlaceholder(repoDir, path string) error {
	return withGitRoot(repoDir, func(root *os.Root) error {
		name := filepath.FromSlash(path)
		if errParents := gitPathParents(root, name, true); errParents != nil {
			return errParents
		}
		file, errCreate := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(errCreate, os.ErrExist) {
			info, errStat := root.Lstat(name)
			if errStat != nil {
				return errStat
			}
			if !info.Mode().IsRegular() || info.Size() != 0 || info.Mode().Perm()&0o111 != 0 {
				return fmt.Errorf("git token store: initialization placeholder %s already has local contents", path)
			}
			return nil
		}
		if errCreate != nil {
			return errCreate
		}
		return file.Close()
	})
}

func ensureGitConfigDir(repoDir string) error {
	return withGitRoot(repoDir, func(root *os.Root) error {
		return gitPathParents(root, filepath.FromSlash(gitConfigPath), true)
	})
}
