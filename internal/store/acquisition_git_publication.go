package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	log "github.com/sirupsen/logrus"
)

type gitPublication struct {
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Staging string `json:"staging"`
}

func publicationHash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

func gitPublicationPath(repoDir, relPath string) string {
	return filepath.Join(repoDir, ".git", "auth-publications", publicationHash([]byte(filepath.ToSlash(relPath)))+".json")
}

// Record intent before Git can push. A process exit cannot lose the worktree
// publication that must follow a durable commit.
func prepareGitPublication(repoDir, relPath, source string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	path := gitPublicationPath(repoDir, relPath)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return codex.WriteJSONFileAtomic(path, map[string]any{"path": filepath.ToSlash(relPath), "sha256": publicationHash(data), "staging": source})
}

func clearGitPublication(repoDir, relPath string) {
	if err := os.Remove(gitPublicationPath(repoDir, relPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Warnf("git token store: could not clear credential publication record: %v", err)
	}
}

func recoverGitPublications(ctx context.Context, repo *git.Repository, repoDir, authDir string) error {
	dir := filepath.Join(repoDir, ".git", "auth-publications")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		marker := filepath.Join(dir, entry.Name())
		record, err := readGitPublication(marker, repoDir, authDir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		destination := filepath.Join(repoDir, filepath.FromSlash(record.Path))
		if err := codex.WithFileLockContext(ctx, destination, func() error {
			// Another process may have finished publishing while this startup waited.
			record, err := readGitPublication(marker, repoDir, authDir)
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			head, err := repo.Head()
			if err != nil {
				return err
			}
			remote, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", head.Name().Short()), true)
			if err != nil {
				return err
			}
			if head.Hash() != remote.Hash() {
				return fmt.Errorf("pending credential publication requires a synchronized remote branch")
			}
			commit, err := repo.CommitObject(head.Hash())
			if err != nil {
				return err
			}
			file, err := commit.File(record.Path)
			if errors.Is(err, object.ErrFileNotFound) {
				finishGitPublicationReplay(repoDir, destination, record)
				return nil
			}
			if err != nil {
				return err
			}
			content, err := file.Contents()
			if err != nil {
				return err
			}
			if publicationHash([]byte(content)) == record.SHA256 {
				if err := publishGitCredential(destination, []byte(content)); err != nil {
					return err
				}
			}
			// A different remote version must not activate the uncommitted staged one.
			finishGitPublicationReplay(repoDir, destination, record)
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

func finishGitPublicationReplay(repoDir, destination string, record gitPublication) {
	clearGitPublication(repoDir, record.Path)
	stagingDir := filepath.Dir(record.Staging)
	if filepath.Base(record.Staging) == "credential" && filepath.Dir(stagingDir) == filepath.Dir(destination) && strings.HasPrefix(filepath.Base(stagingDir), ".auth-acquisition-") {
		if err := os.RemoveAll(stagingDir); err != nil {
			log.Warnf("git token store: could not clean credential staging directory: %v", err)
		}
	}
}

func readGitPublication(marker, repoDir, authDir string) (gitPublication, error) {
	var record gitPublication
	data, err := os.ReadFile(marker)
	if err != nil {
		return record, err
	}
	if err := json.Unmarshal(data, &record); err != nil {
		return record, err
	}
	paths, err := normalizeManagedPaths([]string{record.Path})
	if err != nil || len(paths) != 1 || paths[0] != record.Path {
		return record, fmt.Errorf("invalid credential publication path")
	}
	relative, err := filepath.Rel(authDir, filepath.Join(repoDir, filepath.FromSlash(record.Path)))
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return record, fmt.Errorf("credential publication is outside auth directory")
	}
	if marker != gitPublicationPath(repoDir, record.Path) {
		return record, fmt.Errorf("credential publication record does not match path")
	}
	hash, err := hex.DecodeString(record.SHA256)
	if err != nil || len(hash) != sha256.Size {
		return record, fmt.Errorf("invalid credential publication digest")
	}
	return record, nil
}

func publishGitCredential(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".auth-publication-*")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer func() { _ = os.Remove(temp) }()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temp, path)
}
