package store

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	gitindex "github.com/go-git/go-git/v6/plumbing/format/index"
	"github.com/go-git/go-git/v6/plumbing/object"
)

// gitCheckoutState describes a file, symlink, or directory. A disk snapshot
// also carries its identity; a nil state denotes an absent path.
type gitCheckoutState struct {
	info os.FileInfo
	mode os.FileMode
	data []byte
}

type gitCheckoutUpdate struct {
	name          string
	before, after *gitCheckoutState
}

func readGitCheckoutState(root *os.Root, name string) (*gitCheckoutState, error) {
	info, errStat := root.Lstat(name)
	if errors.Is(errStat, os.ErrNotExist) {
		return nil, nil
	}
	if errStat != nil {
		return nil, errStat
	}
	state := &gitCheckoutState{info: info, mode: info.Mode()}
	switch {
	case state.mode.IsRegular():
		data, errRead := root.ReadFile(name)
		if errRead != nil {
			return nil, errRead
		}
		state.data = data
	case state.mode&os.ModeSymlink != 0:
		target, errLink := root.Readlink(name)
		if errLink != nil {
			return nil, errLink
		}
		state.data = []byte(target)
	}
	current, errCurrent := root.Lstat(name)
	if errCurrent != nil {
		return nil, errCurrent
	}
	if !os.SameFile(info, current) || info.Mode() != current.Mode() || info.Size() != current.Size() || !info.ModTime().Equal(current.ModTime()) {
		return nil, gitCheckoutConflict(name)
	}
	return state, nil
}

func gitCheckoutBlob(tree *object.Tree, entries []*gitindex.Entry) (*gitCheckoutState, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	entry := entries[0]
	mode, errMode := entry.Mode.ToOSFileMode()
	if errMode != nil || (!mode.IsRegular() && mode&os.ModeSymlink == 0) {
		return nil, fmt.Errorf("unsupported git mode %s for %s", entry.Mode, entry.Name)
	}
	file, errFile := tree.File(entry.Name)
	if errFile != nil {
		return nil, errFile
	}
	data, errRead := file.Contents()
	if errRead != nil {
		return nil, errRead
	}
	return &gitCheckoutState{mode: mode, data: []byte(data)}, nil
}

func gitCheckoutConflict(name string) error {
	return fmt.Errorf("remote path %s conflicts with a local worktree change", name)
}

// planGitCheckout preflights all ordinary paths before any mutation. Only
// remove directories emptied by this checkout; keep untracked empty dirs and
// directories that will receive paths in the new tree.
func planGitCheckout(root *os.Root, base, next *object.Tree, paths []string, oldPaths, nextPaths map[string][]*gitindex.Entry, createConfigParent bool) ([]gitCheckoutUpdate, error) {
	var removes, mkdirs, writes []gitCheckoutUpdate
	removed, created := make(map[string]bool), make(map[string]bool)
	for _, path := range paths {
		name := filepath.FromSlash(path)
		if !validGitWorktreePath(name) {
			return nil, fmt.Errorf("invalid git worktree path %q", path)
		}
		old, errOld := gitCheckoutBlob(base, oldPaths[path])
		if errOld != nil {
			return nil, errOld
		}
		nextFile, errNext := gitCheckoutBlob(next, nextPaths[path])
		if errNext != nil {
			return nil, errNext
		}
		var before *gitCheckoutState
		if old != nil {
			if errParents := gitPathParents(root, name, false); errParents != nil {
				return nil, errParents
			}
			var errRead error
			before, errRead = readGitCheckoutState(root, name)
			if errRead != nil {
				return nil, errRead
			}
			if before == nil || before.mode.Type() != old.mode.Type() ||
				(old.mode.IsRegular() && before.mode&0o100 != old.mode&0o100) || !bytes.Equal(before.data, old.data) {
				return nil, gitCheckoutConflict(name)
			}
		}
		update := gitCheckoutUpdate{name: name, before: before, after: nextFile}
		if nextFile != nil {
			writes = append(writes, update)
		} else if before != nil {
			removes = append(removes, update)
			removed[name] = true
		} else {
			return nil, fmt.Errorf("invalid git worktree transition at %q", path)
		}
	}

	var removeDir func(string, os.FileInfo) error
	removeDir = func(name string, info os.FileInfo) error {
		children, errRead := readGitCheckoutDir(root, name)
		if errRead != nil {
			return errRead
		}
		if len(children) == 0 {
			return gitCheckoutConflict(name)
		}
		for _, child := range children {
			childName := filepath.Join(name, child.Name())
			if removed[childName] {
				continue
			}
			childInfo, errInfo := child.Info()
			if errInfo != nil {
				return errInfo
			}
			if !childInfo.IsDir() {
				return gitCheckoutConflict(childName)
			}
			if errChild := removeDir(childName, childInfo); errChild != nil {
				return errChild
			}
		}
		removes = append(removes, gitCheckoutUpdate{name: name, before: &gitCheckoutState{info: info, mode: info.Mode()}})
		removed[name] = true
		return nil
	}

	neededDirs := make(map[string]bool)
	var ensureDir func(string) error
	ensureDir = func(dir string) error {
		if dir == "." || neededDirs[dir] {
			return nil
		}
		neededDirs[dir] = true
		parent := filepath.Dir(dir)
		if errParent := ensureDir(parent); errParent != nil {
			return errParent
		}
		if !removed[dir] && !created[parent] {
			info, errStat := root.Lstat(dir)
			if errStat == nil {
				if !info.IsDir() {
					return gitCheckoutConflict(dir)
				}
				return nil
			}
			if !errors.Is(errStat, os.ErrNotExist) {
				return errStat
			}
		}
		mkdirs = append(mkdirs, gitCheckoutUpdate{name: dir, after: &gitCheckoutState{mode: os.ModeDir | 0o700}})
		created[dir] = true
		return nil
	}

	for _, update := range writes {
		parent := filepath.Dir(update.name)
		if errParents := ensureDir(parent); errParents != nil {
			return nil, errParents
		}
		if update.before != nil || created[parent] {
			continue
		}
		info, errStat := root.Lstat(update.name)
		if errStat == nil && info.IsDir() {
			if errRemove := removeDir(update.name, info); errRemove != nil {
				return nil, errRemove
			}
		} else if errStat == nil {
			return nil, gitCheckoutConflict(update.name)
		} else if !errors.Is(errStat, os.ErrNotExist) {
			return nil, errStat
		}
	}
	if createConfigParent {
		if errParent := ensureDir(filepath.Dir(filepath.FromSlash(gitConfigPath))); errParent != nil {
			return nil, errParent
		}
	}
	prunes, errPrune := planGitCheckoutPrunes(root, removes, removed, neededDirs)
	if errPrune != nil {
		return nil, errPrune
	}
	return slices.Concat(removes, prunes, mkdirs, writes), nil
}

func planGitCheckoutPrunes(root *os.Root, removes []gitCheckoutUpdate, removed, needed map[string]bool) ([]gitCheckoutUpdate, error) {
	parents := make(map[string]bool)
	for _, update := range removes {
		for dir := filepath.Dir(update.name); dir != "." && !needed[dir] && !parents[dir]; dir = filepath.Dir(dir) {
			parents[dir] = true
		}
	}
	var prunes []gitCheckoutUpdate
	// Descendants sort after their parents. Removal must happen bottom-up so
	// that rollback can recreate the directories before restoring their files.
	for _, dir := range slices.Backward(slices.Sorted(maps.Keys(parents))) {
		if removed[dir] {
			continue
		}
		info, errStat := root.Lstat(dir)
		if errStat != nil {
			return nil, errStat
		}
		if !info.IsDir() {
			return nil, gitCheckoutConflict(dir)
		}
		entries, errRead := readGitCheckoutDir(root, dir)
		if errRead != nil {
			return nil, errRead
		}
		if len(entries) == 0 {
			continue
		}
		if slices.ContainsFunc(entries, func(entry os.DirEntry) bool { return !removed[filepath.Join(dir, entry.Name())] }) {
			continue
		}
		prunes = append(prunes, gitCheckoutUpdate{name: dir, before: &gitCheckoutState{info: info, mode: info.Mode()}})
		removed[dir] = true
	}
	return prunes, nil
}

func readGitCheckoutDir(root *os.Root, name string) (entries []os.DirEntry, errResult error) {
	dir, errOpen := root.Open(name)
	if errOpen != nil {
		return nil, errOpen
	}
	defer func() { errResult = errors.Join(errResult, dir.Close()) }()
	return dir.ReadDir(-1)
}

// applyGitCheckout compensates successful steps in reverse order on failure.
// Undo only a file that still matches what this attempt wrote; a user edit or
// a replacement at that path is a conflict, not ours to roll back.
func applyGitCheckout(root *os.Root, updates []gitCheckoutUpdate, finish func() error) (errResult error) {
	undo := make([]gitCheckoutUpdate, 0, len(updates))
	defer func() {
		if errResult == nil {
			return
		}
		for _, update := range slices.Backward(undo) {
			if _, errUndo := update.apply(root); errUndo != nil {
				errResult = errors.Join(errResult, fmt.Errorf("restore git worktree path %s: %w", update.name, errUndo))
			}
		}
	}()
	for _, update := range updates {
		installed, errApply := update.apply(root)
		if errApply == nil || installed != nil {
			undo = append(undo, gitCheckoutUpdate{name: update.name, before: installed, after: update.before})
		}
		if errApply != nil {
			return fmt.Errorf("apply git worktree path %s: %w", update.name, errApply)
		}
	}
	return finish()
}

func (u gitCheckoutUpdate) check(root *os.Root) error {
	if errParents := gitPathParents(root, u.name, false); errParents != nil {
		return errParents
	}
	current, errRead := readGitCheckoutState(root, u.name)
	if errRead != nil {
		return errRead
	}
	if (current == nil) != (u.before == nil) || (current != nil &&
		(!os.SameFile(current.info, u.before.info) || current.mode != u.before.mode || !bytes.Equal(current.data, u.before.data))) {
		return gitCheckoutConflict(u.name)
	}
	return nil
}

func (u gitCheckoutUpdate) apply(root *os.Root) (installed *gitCheckoutState, errResult error) {
	if errCheck := u.check(root); errCheck != nil {
		return nil, errCheck
	}
	if u.after == nil {
		return nil, root.Remove(u.name)
	}
	if u.after.mode.IsDir() {
		if errMkdir := root.Mkdir(u.name, u.after.mode.Perm()); errMkdir != nil {
			return nil, errMkdir
		}
		info, errStat := root.Lstat(u.name)
		if errStat != nil {
			return nil, errStat
		}
		if !info.IsDir() {
			return nil, gitCheckoutConflict(u.name)
		}
		installed := &gitCheckoutState{info: info, mode: info.Mode()}
		const permissions = os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky
		if u.after.info != nil && info.Mode()&permissions != u.after.mode&permissions {
			// Restore directory permissions even when the current umask differs.
			dir, errOpen := root.Open(u.name)
			if errOpen != nil {
				return installed, errOpen
			}
			opened, errRestore := dir.Stat()
			if errRestore == nil && !os.SameFile(info, opened) {
				errRestore = gitCheckoutConflict(u.name)
			}
			if errRestore == nil {
				errRestore = dir.Chmod(u.after.mode & permissions)
			}
			return installed, errors.Join(errRestore, dir.Close())
		}
		return installed, nil
	}

	temp := filepath.Join(filepath.Dir(u.name), ".gitstore-"+rand.Text())
	defer func() {
		if temp != "" {
			if errRemove := root.Remove(temp); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
				errResult = errors.Join(errResult, errRemove)
			}
		}
	}()
	if u.after.mode&os.ModeSymlink != 0 {
		if errLink := root.Symlink(string(u.after.data), temp); errLink != nil {
			temp = ""
			return nil, errLink
		}
	} else {
		file, errOpen := root.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, u.after.mode.Perm())
		if errOpen != nil {
			temp = ""
			return nil, errOpen
		}
		_, errWrite := file.Write(u.after.data)
		if errWrite == nil && u.after.info != nil {
			// Restore the original permissions rather than applying umask again.
			errWrite = file.Chmod(u.after.mode.Perm())
		}
		if errWrite = errors.Join(errWrite, file.Close()); errWrite != nil {
			return nil, errWrite
		}
	}
	info, errStat := root.Lstat(temp)
	if errStat != nil {
		return nil, errStat
	}
	if u.before == nil {
		// Unlike rename, link cannot replace a concurrently created file.
		if errLink := root.Link(temp, u.name); errLink != nil {
			return nil, errLink
		}
	} else {
		if errCheck := u.check(root); errCheck != nil {
			return nil, errCheck
		}
		if errRename := root.Rename(temp, u.name); errRename != nil {
			return nil, errRename
		}
		temp = ""
	}
	return &gitCheckoutState{info: info, mode: info.Mode(), data: u.after.data}, nil
}
