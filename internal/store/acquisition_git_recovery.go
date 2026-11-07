package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
)

// A staged acquisition can move HEAD before its push. Undo only a journal-matched
// credential commit, preserving the worktree, before ordinary pull conflict checks.
func (s *GitTokenStore) recoverUnpushedPublications(ctx context.Context, repo *git.Repository, worktree *git.Worktree, repoDir, authDir string) error {
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
			record, err := readGitPublication(marker, repoDir, authDir)
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			// Fetch after taking the publication lock. A push may have completed while
			// this startup was waiting for the writer that owned the journal record.
			if err := syncRemoteReferences(ctx, repo, s.gitClientOptions()); err != nil {
				return err
			}
			head, err := repo.Head()
			if err != nil {
				return err
			}
			if !head.Name().IsBranch() {
				return fmt.Errorf("pending credential publication requires a branch")
			}
			remote, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", head.Name().Short()), true)
			if err != nil {
				return err
			}
			if head.Hash() == remote.Hash() {
				return nil
			}
			commit, err := repo.CommitObject(head.Hash())
			if err != nil {
				return err
			}
			file, err := commit.File(record.Path)
			if errors.Is(err, object.ErrFileNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			content, err := file.Contents()
			if err != nil {
				return err
			}
			if publicationHash([]byte(content)) != record.SHA256 {
				return nil
			}
			// Remote can advance after the interrupted save. Its original base defines
			// which local changes belong to this acquisition.
			base := remote.Hash()
			if record.Base != "" {
				base = plumbing.NewHash(record.Base)
			}
			if err := validateManagedTreeChanges(repo, base, head.Hash(), []string{record.Path}); err != nil {
				return fmt.Errorf("pending credential commit includes changes outside its recorded path: %w", err)
			}
			return restoreHeadAndIndex(repo, worktree, plumbing.NewHashReference(head.Name(), base))
		}); err != nil {
			return err
		}
	}
	return nil
}
