package store

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
)

func (s *GitTokenStore) commitAuthSourceLocked(ctx context.Context, message, relPath, path, source string) error {
	if source == path {
		return s.commitAndPushContextLocked(ctx, message, relPath)
	}
	repoDir := s.repoDirSnapshot()
	if err := prepareGitPublication(repoDir, relPath, source); err != nil {
		return err
	}
	// A failed save may also have failed to restore HEAD or the index. Keep the
	// journal until recovery has inspected the actual Git state.
	return s.commitSourcesAndPushLocked(ctx, message, false, map[string]string{filepath.ToSlash(relPath): source}, relPath)
}

// Insert the private staged content directly into Git's index. Worktree.Add
// would require publishing an uncommitted credential in the watched directory.
func stageAcquisitionBlob(repo *git.Repository, relPath, source string) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("git token store: read staged credential: %w", err)
	}
	obj := repo.Storer.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	obj.SetSize(int64(len(data)))
	writer, err := obj.Writer()
	if err != nil {
		return err
	}
	if _, err := writer.Write(data); err != nil {
		_ = writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	hash, err := repo.Storer.SetEncodedObject(obj)
	if err != nil {
		return err
	}
	index, err := repo.Storer.Index()
	if err != nil {
		return err
	}
	entry, err := index.Entry(relPath)
	if err != nil {
		entry, err = index.Add(relPath)
		if err != nil {
			return err
		}
	}
	entry.Hash = hash
	entry.Mode = filemode.Regular
	entry.Size = uint32(len(data))
	return repo.Storer.SetIndex(index)
}
