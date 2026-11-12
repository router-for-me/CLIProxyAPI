package store

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/go-git/go-git/v6/storage/filesystem"
	"github.com/go-git/go-git/v6/storage/memory"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codex"
	log "github.com/sirupsen/logrus"
)

// Refresh journals use immutable tags, not credential-branch writes. Ordinary
// pulls, mirror recovery, and auth saves therefore cannot erase a claim. Each
// claimant creates a different root commit; a non-force push can publish only one.
func (s *GitTokenStore) RefreshClaim(ctx context.Context, key string) (claimed bool, errResult error) {
	errResult = s.withRefreshRepository(ctx, func(repo *git.Repository) error {
		ref := refreshGitRef("claim", key)
		if hash, errLookup := s.refreshRemoteRef(ctx, repo, ref); errLookup != nil {
			return errLookup
		} else if !hash.IsZero() {
			return nil
		}
		nonce := make([]byte, 32)
		if _, errRand := rand.Read(nonce); errRand != nil {
			return errRand
		}
		createdHash, errPublish := s.publishRefreshRecord(ctx, repo, ref, fmt.Sprintf("refresh claim %x", nonce))
		if errPublish == nil {
			s.refreshClaims.Store(key, createdHash)
			claimed = true
			return nil
		}
		// A conflicting or uncertain push never grants permission to send OAuth.
		if hash, errLookup := s.refreshRemoteRef(ctx, repo, ref); errLookup == nil && !hash.IsZero() {
			return nil
		}
		return errPublish
	})
	return claimed, errResult
}

func (s *GitTokenStore) RefreshRead(ctx context.Context, key string) (receipt []byte, errResult error) {
	errResult = s.withRefreshRepository(ctx, func(repo *git.Repository) error {
		ref := refreshGitRef("receipt", key)
		hash, errLookup := s.refreshRemoteRef(ctx, repo, ref)
		if errLookup != nil {
			return errLookup
		}
		if hash.IsZero() {
			claim, errClaim := s.refreshRemoteRef(ctx, repo, refreshGitRef("claim", key))
			if errClaim != nil {
				return errClaim
			}
			if !claim.IsZero() {
				receipt = []byte(codex.PendingRefreshReceipt)
			}
			return nil
		}
		if errFetch := repo.FetchContext(ctx, &git.FetchOptions{RemoteName: "origin", ClientOptions: s.gitClientOptions(), RefSpecs: []config.RefSpec{config.RefSpec(ref.String() + ":" + ref.String())}, Tags: git.NoTags}); errFetch != nil && !errors.Is(errFetch, git.NoErrAlreadyUpToDate) {
			return errFetch
		}
		commit, errCommit := repo.CommitObject(hash)
		if errCommit != nil {
			return errCommit
		}
		receipt = []byte(commit.Message)
		return nil
	})
	return receipt, errResult
}

func (s *GitTokenStore) RefreshComplete(ctx context.Context, key string, receipt []byte) error {
	s.refreshClaims.Delete(key)
	return s.withRefreshRepository(ctx, func(repo *git.Repository) error {
		_, errPublish := s.publishRefreshRecord(ctx, repo, refreshGitRef("receipt", key), string(receipt))
		return errPublish
	})
}

func (s *GitTokenStore) RefreshRelease(ctx context.Context, key string) error {
	if s == nil {
		return fmt.Errorf("git refresh journal is unavailable")
	}
	ownedVal, owned := s.refreshClaims.LoadAndDelete(key)
	if !owned {
		return fmt.Errorf("refresh claim is not owned by this store")
	}
	ownedHash, okHash := ownedVal.(plumbing.Hash)
	if !okHash || ownedHash.IsZero() {
		return fmt.Errorf("invalid owned refresh claim hash")
	}
	return s.withRefreshRepository(ctx, func(repo *git.Repository) error {
		receiptHash, errReceipt := s.refreshRemoteRef(ctx, repo, refreshGitRef("receipt", key))
		if errReceipt != nil {
			return errReceipt
		}
		if !receiptHash.IsZero() {
			return fmt.Errorf("refresh receipt already exists")
		}
		claimRef := refreshGitRef("claim", key)
		claimHash, errClaim := s.refreshRemoteRef(ctx, repo, claimRef)
		if errClaim != nil {
			return errClaim
		}
		if claimHash != ownedHash {
			return fmt.Errorf("refresh claim ownership changed")
		}
		errPush := repo.PushContext(ctx, &git.PushOptions{
			RemoteName:    "origin",
			ClientOptions: s.gitClientOptions(),
			RefSpecs:      []config.RefSpec{config.RefSpec(":" + claimRef.String())},
		})
		if errors.Is(errPush, git.NoErrAlreadyUpToDate) {
			return nil
		}
		return errPush
	})
}

func refreshGitRef(kind, key string) plumbing.ReferenceName {
	return plumbing.ReferenceName("refs/tags/cliproxy-refresh/" + kind + "/" + key)
}

func (s *GitTokenStore) withRefreshRepository(ctx context.Context, run func(*git.Repository) error) (errResult error) {
	if s == nil || strings.TrimSpace(s.remote) == "" {
		return fmt.Errorf("git refresh journal is unavailable")
	}
	// Refresh can hold a credential file lock while Save holds s.mu waiting
	// for that same file. Journal I/O must neither acquire s.mu nor touch the
	// credential mirror. Its immutable remote records need only a temporary
	// object database, not a checkout of the credential branch.
	repo, errInit := git.Init(memory.NewStorage(), nil)
	if errInit != nil {
		return errInit
	}
	defer func() { errResult = errors.Join(errResult, repo.Close()) }()
	if _, errRemote := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{s.remote}}); errRemote != nil {
		return errRemote
	}
	// go-git's in-process file receive-pack does not serialize competing
	// reference updates. Lock on the shared remote itself for that transport.
	endpoint, errParse := transport.ParseURL(s.remote)
	if errParse != nil {
		return errParse
	}
	if endpoint.Scheme == "file" {
		// Use the transport's own resolution of bare repositories, .git
		// directories and gitfiles so aliases share the same physical lock.
		remoteStorage, errLoad := transport.DefaultLoader.Load(endpoint)
		if errLoad != nil {
			return errLoad
		}
		remoteFilesystem, ok := remoteStorage.(*filesystem.Storage)
		if !ok {
			return fmt.Errorf("local git refresh journal requires filesystem storage")
		}
		defer func() { errResult = errors.Join(errResult, remoteFilesystem.Close()) }()
		canonical, errCanonical := filepath.EvalSymlinks(remoteFilesystem.Filesystem().Root())
		if errCanonical != nil {
			return errCanonical
		}
		return codex.WithFileLock(filepath.Join(canonical, ".cliproxy-refresh"), func() error { return run(repo) })
	}
	return run(repo)
}

func (s *GitTokenStore) refreshRemoteRef(ctx context.Context, repo *git.Repository, name plumbing.ReferenceName) (plumbing.Hash, error) {
	remote, errRemote := repo.Remote("origin")
	if errRemote != nil {
		return plumbing.ZeroHash, errRemote
	}
	refs, errList := remote.ListContext(ctx, &git.ListOptions{ClientOptions: s.gitClientOptions()})
	if errList != nil {
		return plumbing.ZeroHash, errList
	}
	for _, ref := range refs {
		if ref.Name() == name {
			return ref.Hash(), nil
		}
	}
	return plumbing.ZeroHash, nil
}

func (s *GitTokenStore) publishRefreshRecord(ctx context.Context, repo *git.Repository, ref plumbing.ReferenceName, message string) (plumbing.Hash, error) {
	tree := &object.Tree{}
	treeObject := repo.Storer.NewEncodedObject()
	if errEncode := tree.Encode(treeObject); errEncode != nil {
		return plumbing.ZeroHash, errEncode
	}
	treeHash, errTree := repo.Storer.SetEncodedObject(treeObject)
	if errTree != nil {
		return plumbing.ZeroHash, errTree
	}
	signature := object.Signature{Name: "CLIProxyAPI", Email: "refresh@cliproxyapi.local", When: time.Now()}
	commit := &object.Commit{Author: signature, Committer: signature, Message: message, TreeHash: treeHash}
	encoded := repo.Storer.NewEncodedObject()
	if errEncode := commit.Encode(encoded); errEncode != nil {
		return plumbing.ZeroHash, errEncode
	}
	hash, errStore := repo.Storer.SetEncodedObject(encoded)
	if errStore != nil {
		return plumbing.ZeroHash, errStore
	}
	// A unique local source avoids updating a tag fetched from another claimant.
	localRef := plumbing.ReferenceName("refs/cliproxy-refresh-pending/" + hash.String())
	if errReference := repo.Storer.SetReference(plumbing.NewHashReference(localRef, hash)); errReference != nil {
		return plumbing.ZeroHash, errReference
	}
	defer func() {
		if errRemove := repo.Storer.RemoveReference(localRef); errRemove != nil {
			log.Warnf("remove temporary refresh journal reference: %v", errRemove)
		}
	}()
	if errPush := repo.PushContext(ctx, &git.PushOptions{RemoteName: "origin", ClientOptions: s.gitClientOptions(), RefSpecs: []config.RefSpec{config.RefSpec(localRef.String() + ":" + ref.String())}}); errPush != nil {
		return plumbing.ZeroHash, errPush
	}
	return hash, nil
}
