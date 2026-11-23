package store

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/go-git/go-billy/v6"
	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/filemode"
	format "github.com/go-git/go-git/v6/plumbing/format/config"
	gitindex "github.com/go-git/go-git/v6/plumbing/format/index"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/storage"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	log "github.com/sirupsen/logrus"
)

func gitTreeAt(repo *git.Repository, oid plumbing.Hash) (*object.Tree, error) {
	if oid.IsZero() {
		return &object.Tree{}, nil
	}
	commit, errCommit := repo.CommitObject(oid)
	if errCommit != nil {
		return nil, errCommit
	}
	return commit.Tree()
}

func indexFromGitTree(tree *object.Tree) (*gitindex.Index, error) {
	idx := &gitindex.Index{Version: 2}
	walker := object.NewTreeWalker(tree, true, nil)
	defer walker.Close()
	for {
		name, entry, errNext := walker.Next()
		if errors.Is(errNext, io.EOF) {
			return idx, nil
		}
		if errNext != nil {
			return nil, errNext
		}
		if entry.Mode != filemode.Dir {
			idx.Entries = append(idx.Entries, &gitindex.Entry{Name: name, Hash: entry.Hash, Mode: entry.Mode})
		}
	}
}

func indexedPaths(idx *gitindex.Index) map[string][]*gitindex.Entry {
	paths := make(map[string][]*gitindex.Entry, len(idx.Entries))
	for _, entry := range idx.Entries {
		paths[entry.Name] = append(paths[entry.Name], entry)
	}
	return paths
}

func sameGitEntries(a, b []*gitindex.Entry) bool {
	return slices.EqualFunc(a, b, func(a, b *gitindex.Entry) bool {
		return a.Hash == b.Hash && a.Mode == b.Mode && a.Stage == b.Stage && a.IntentToAdd == b.IntentToAdd && a.SkipWorktree == b.SkipWorktree
	})
}

// gitCandidateStorage isolates temporary staging and commits from the user's
// live index and references. The only shared writes are immutable objects.
type gitCandidateStorage struct {
	storage.Storer
	index *gitindex.Index
	refs  map[plumbing.ReferenceName]*plumbing.Reference
}

func (s *gitCandidateStorage) Index() (*gitindex.Index, error) { return s.index, nil }
func (s *gitCandidateStorage) SetIndex(idx *gitindex.Index) error {
	s.index = idx
	return nil
}
func (s *gitCandidateStorage) Reference(name plumbing.ReferenceName) (*plumbing.Reference, error) {
	if ref := s.refs[name]; ref != nil {
		return ref, nil
	}
	return nil, plumbing.ErrReferenceNotFound
}
func (s *gitCandidateStorage) SetReference(ref *plumbing.Reference) error {
	s.refs[ref.Name()] = ref
	return nil
}

// Git status can hash every tracked file, even when adding just one path.
// Supply the config from a locked snapshot, not an extra descriptor: on
// Solaris/AIX, closing one descriptor can release this process's fcntl lock.
type gitScanFS struct {
	billy.Filesystem
	config billy.Filesystem
}

func (f gitScanFS) Open(name string) (billy.File, error) {
	if filepath.ToSlash(filepath.Clean(name)) == gitConfigPath {
		return f.config.Open(gitConfigPath)
	}
	return f.Filesystem.Open(name)
}

func gitSnapshotFS(fs billy.Filesystem, snapshot *config.ConfigFileSnapshot) (billy.Filesystem, error) {
	memory := memfs.New()
	if snapshot != nil {
		file, errCreate := memory.Create(gitConfigPath)
		if errCreate != nil {
			return nil, errCreate
		}
		_, errWrite := file.Write(snapshot.Bytes())
		if errWrite = errors.Join(errWrite, file.Close()); errWrite != nil {
			return nil, errWrite
		}
	}
	return gitScanFS{Filesystem: fs, config: memory}, nil
}

func worktreeDirtyPaths(repo *git.Repository, snapshot *config.ConfigFileSnapshot) (map[string]struct{}, error) {
	worktree, errWorktree := repo.Worktree()
	if errWorktree != nil {
		return nil, errWorktree
	}
	fs, errSnapshot := gitSnapshotFS(worktree.Filesystem(), snapshot)
	if errSnapshot != nil {
		return nil, errSnapshot
	}
	scanRepo, errOpen := git.Open(repo.Storer, fs)
	if errOpen != nil {
		return nil, errOpen
	}
	worktree, errWorktree = scanRepo.Worktree()
	if errWorktree != nil {
		return nil, errWorktree
	}
	dirty := make(map[string]struct{})
	errStatus := config.WithConfigFileOpenGate(func() error {
		status, errStatus := worktree.Status()
		if errStatus != nil {
			return errStatus
		}
		for path, entry := range status {
			if entry.Staging != git.Unmodified || entry.Worktree != git.Unmodified {
				dirty[filepath.ToSlash(filepath.Clean(path))] = struct{}{}
			}
		}
		return nil
	})
	return dirty, errStatus
}

// gitFileUpdate uses Git's lockfile protocol. Keep the lock from reading the
// original value through publishing the replacement, including packed refs.
type gitFileUpdate struct {
	file *os.File
	path string
}

func lockGitFile(path string) (*gitFileUpdate, error) {
	if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
		return nil, errMkdir
	}
	file, errOpen := os.OpenFile(path+".lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errOpen != nil {
		return nil, errOpen
	}
	return &gitFileUpdate{file: file, path: path}, nil
}

func (u *gitFileUpdate) close() {
	if u.file != nil {
		if errClose := u.file.Close(); errClose != nil {
			log.Warnf("git token store: close %s.lock: %v", u.path, errClose)
		}
	}
	if u.path != "" {
		if errRemove := os.Remove(u.path + ".lock"); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
			log.Warnf("git token store: remove %s.lock: %v", u.path, errRemove)
		}
	}
}

func (u *gitFileUpdate) publish() error {
	errClose := u.file.Close()
	u.file = nil
	if errClose != nil {
		return errClose
	}
	if errRename := os.Rename(u.path+".lock", u.path); errRename != nil {
		return errRename
	}
	u.path = ""
	return nil
}

func (u *gitFileUpdate) writeIndex(repo *git.Repository, idx *gitindex.Index) error {
	cfg, errConfig := repo.Config()
	if errConfig != nil {
		return errConfig
	}
	var h hash.Hash = sha1.New()
	if cfg.Extensions.ObjectFormat == format.SHA256 {
		h = sha256.New()
	}
	// Cached index extensions describe the old entries, not this replacement.
	return gitindex.NewEncoder(u.file, h).Encode(&gitindex.Index{Version: idx.Version, Entries: idx.Entries})
}

func gitHeadBranch(repo *git.Repository) (plumbing.ReferenceName, plumbing.Hash, error) {
	head, errHead := repo.Reference(plumbing.HEAD, false)
	if errHead != nil {
		return "", plumbing.ZeroHash, errHead
	}
	if head.Type() != plumbing.SymbolicReference || !head.Target().IsBranch() {
		return "", plumbing.ZeroHash, fmt.Errorf("git token store: HEAD is not a branch")
	}
	oid, errBranch := gitRefHash(repo, head.Target())
	return head.Target(), oid, errBranch
}

func gitRefHash(repo *git.Repository, name plumbing.ReferenceName) (plumbing.Hash, error) {
	ref, errRef := repo.Reference(name, false)
	if errors.Is(errRef, plumbing.ErrReferenceNotFound) {
		return plumbing.ZeroHash, nil
	}
	if errRef != nil {
		return plumbing.ZeroHash, errRef
	}
	if ref.Type() != plumbing.HashReference {
		return plumbing.ZeroHash, fmt.Errorf("git token store: %s is not a direct reference", name)
	}
	return ref.Hash(), nil
}

func gitSyncedRef(branch plumbing.ReferenceName) plumbing.ReferenceName {
	return plumbing.ReferenceName("refs/cliproxy/synced/" + branch.Short())
}

func recordGitSync(repo *git.Repository, branch plumbing.ReferenceName, oid plumbing.Hash) error {
	return repo.Storer.SetReference(plumbing.NewHashReference(gitSyncedRef(branch), oid))
}

// updateGitWorktree changes only index entries belonging to the transition.
// A staged user edit is never replaced, even on a path selected for upload.
// A rejected upload never calls this function at all.
func updateGitWorktree(repo *git.Repository, repoDir string, from, to plumbing.ReferenceName, oldOID, targetOldOID, newOID plumbing.Hash, checkout bool, seed *config.ConfigFileSnapshot) error {
	if errValidate := to.Validate(); errValidate != nil {
		return errValidate
	}
	gitDir := filepath.Join(repoDir, ".git")
	// Git worktrees with a shared .git pointer need a separate lock layout.
	if info, errStat := os.Lstat(gitDir); errStat != nil || !info.IsDir() {
		return fmt.Errorf("git token store: expected a standalone .git directory")
	}
	indexLock, errIndex := lockGitFile(filepath.Join(gitDir, "index"))
	if errIndex != nil {
		return errIndex
	}
	defer indexLock.close()
	headLock, errHead := lockGitFile(filepath.Join(gitDir, "HEAD"))
	if errHead != nil {
		return errHead
	}
	defer headLock.close()
	branchLock, errBranch := lockGitFile(filepath.Join(gitDir, filepath.FromSlash(to.String())))
	if errBranch != nil {
		return errBranch
	}
	defer branchLock.close()
	branch, oid, errCurrent := gitHeadBranch(repo)
	if errCurrent != nil || branch != from || oid != oldOID {
		return errors.Join(errGitLocalAdvanced, errCurrent)
	}
	targetOID, errTarget := gitRefHash(repo, to)
	if errTarget != nil || targetOID != targetOldOID {
		return errors.Join(errGitLocalAdvanced, errTarget)
	}
	base, errBase := gitTreeAt(repo, oldOID)
	if errBase != nil {
		return errBase
	}
	next, errNext := gitTreeAt(repo, newOID)
	if errNext != nil {
		return errNext
	}
	baseIndex, errBaseIndex := indexFromGitTree(base)
	if errBaseIndex != nil {
		return errBaseIndex
	}
	nextIndex, errNextIndex := indexFromGitTree(next)
	if errNextIndex != nil {
		return errNextIndex
	}
	liveIndex, errLive := repo.Storer.Index()
	if errLive != nil {
		return errLive
	}
	oldPaths, nextPaths, livePaths := indexedPaths(baseIndex), indexedPaths(nextIndex), indexedPaths(liveIndex)
	changed, errChanged := changedTreePaths(base, next)
	if errChanged != nil {
		return errChanged
	}
	liveIndex.Entries = nil
	for path, entries := range livePaths {
		if _, found := slices.BinarySearch(changed, path); !found {
			liveIndex.Entries = append(liveIndex.Entries, entries...)
		}
	}
	for _, path := range changed {
		entries := livePaths[path]
		if sameGitEntries(oldPaths[path], entries) {
			entries = nextPaths[path]
		} else if checkout {
			return fmt.Errorf("remote path %s conflicts with local staged change", path)
		}
		liveIndex.Entries = append(liveIndex.Entries, entries...)
	}
	if len(changed) != 0 {
		if errEncode := indexLock.writeIndex(repo, liveIndex); errEncode != nil {
			return errEncode
		}
	}
	if targetOID != newOID {
		if _, errWrite := fmt.Fprintln(branchLock.file, newOID); errWrite != nil {
			return errWrite
		}
	}
	if from != to {
		if _, errWrite := fmt.Fprintf(headLock.file, "ref: %s\n", to); errWrite != nil {
			return errWrite
		}
	}
	if checkout {
		if errApply := checkoutGitPaths(repo, repoDir, base, next, changed, oldPaths, nextPaths, livePaths, seed); errApply != nil {
			return errApply
		}
	}
	if len(changed) != 0 {
		if errPublish := indexLock.publish(); errPublish != nil {
			return errPublish
		}
	}
	if targetOID != newOID {
		if errPublish := branchLock.publish(); errPublish != nil {
			return errPublish
		}
	}
	if from != to {
		return headLock.publish()
	}
	return nil
}

func checkoutGitPaths(repo *git.Repository, repoDir string, base, next *object.Tree, changed []string, oldPaths, nextPaths, live map[string][]*gitindex.Entry, seed *config.ConfigFileSnapshot) (errResult error) {
	before, errBefore := gitConfigBlob(base)
	after, errAfter := gitConfigBlob(next)
	if errConfig := errors.Join(errBefore, errAfter); errConfig != nil {
		return errConfig
	}
	root, errRoot := os.OpenRoot(repoDir)
	if errRoot != nil {
		return errRoot
	}
	defer func() { errResult = errors.Join(errResult, root.Close()) }()
	configChanged := slices.Contains(changed, gitConfigPath)
	local, errRead := readGitConfigSnapshot(root)
	if errRead != nil && (configChanged || !errors.Is(errRead, config.ErrConfigFileDirectory)) {
		return errRead
	}
	adoptSeed := configChanged && before == nil && len(live[gitConfigPath]) == 0 && seed != nil && config.IsPreparedConfigFromSeed(seed.Bytes(), after)
	if configChanged {
		if adoptSeed {
			// CAS against the exact pre-push seed, not whatever is there now.
			local = seed
		} else if (local == nil) != (before == nil) || (local != nil && !bytes.Equal(local.Bytes(), before)) {
			return fmt.Errorf("git config conflicts with local change: %w", config.ErrConfigFileChanged)
		}
	}
	paths := make([]string, 0, len(changed))
	for _, path := range changed {
		if path == gitConfigPath {
			continue
		}
		// Adopt identical initialization placeholders without touching them.
		if len(live[path]) == 0 && (path == "auths/.gitkeep" || path == "config/.gitkeep") {
			if file, errFile := next.File(path); errFile == nil && file.Mode == filemode.Regular && file.Size == 0 {
				name := filepath.FromSlash(path)
				if gitPathParents(root, name, false) == nil {
					if info, errStat := root.Lstat(name); errStat == nil && info.Mode().IsRegular() && info.Size() == 0 && info.Mode().Perm()&0o111 == 0 {
						continue
					}
				}
			}
		}
		paths = append(paths, path)
	}
	updates, errPlan := planGitCheckout(root, base, next, paths, oldPaths, nextPaths, configChanged && local == nil && after != nil)
	if errPlan != nil {
		return errPlan
	}
	dirty, errDirty := worktreeDirtyPaths(repo, local)
	if errDirty != nil {
		return errDirty
	}
	if adoptSeed {
		delete(dirty, gitConfigPath)
	}
	if configChanged {
		paths = append(paths, gitConfigPath)
	}
	for _, path := range paths {
		if dirtyPath, conflict := overlappingDirtyPath(path, dirty); conflict {
			return fmt.Errorf("remote path %s conflicts with local change %s", path, dirtyPath)
		}
	}
	return applyGitCheckout(root, updates, func() error {
		if configChanged {
			return applyGitConfigSnapshot(root, local, after)
		}
		return nil
	})
}

func validGitWorktreePath(path string) bool {
	return filepath.IsLocal(path) && path != "." && !strings.EqualFold(strings.Split(filepath.ToSlash(path), "/")[0], ".git")
}

func gitPathParents(root *os.Root, path string, create bool) error {
	if !validGitWorktreePath(path) {
		return fmt.Errorf("invalid git worktree path %q", path)
	}
	parent := filepath.Dir(path)
	if parent == "." {
		return nil
	}
	if errParents := gitPathParents(root, parent, create); errParents != nil {
		return errParents
	}
	info, errStat := root.Lstat(parent)
	if create && errors.Is(errStat, os.ErrNotExist) {
		return root.Mkdir(parent, 0o700)
	}
	if errStat != nil {
		return errStat
	}
	if !info.IsDir() {
		return fmt.Errorf("git worktree parent %q is not a directory", parent)
	}
	return nil
}
