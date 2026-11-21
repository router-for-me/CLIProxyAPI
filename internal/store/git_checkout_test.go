package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestGitCheckoutRollsBackAfterLatePathConflict(t *testing.T) {
	dir := t.TempDir()
	writeSeedTestFile(t, filepath.Join(dir, "old"), "original\n")
	if errRoot := withGitRoot(dir, func(root *os.Root) error {
		before, errRead := readGitCheckoutState(root, "old")
		if errRead != nil {
			return errRead
		}
		updates := []gitCheckoutUpdate{
			{name: "old", before: before, after: &gitCheckoutState{mode: 0o644, data: []byte("remote change\n")}},
			{name: "new", after: &gitCheckoutState{mode: 0o644, data: []byte("remote addition\n")}},
			{name: "blocked", after: &gitCheckoutState{mode: 0o644, data: []byte("remote addition\n")}},
		}
		// The directory appears after preflight observed the path as absent.
		if errMkdir := root.Mkdir("blocked", 0o700); errMkdir != nil {
			return errMkdir
		}
		if errApply := applyGitCheckout(root, updates, func() error {
			t.Error("checkout finished despite a late conflict")
			return nil
		}); errApply == nil {
			t.Fatal("checkout overwrote the conflicting directory")
		}
		assertLocalFileContents(t, filepath.Join(dir, "old"), "original\n")
		if _, errStat := root.Lstat("new"); !errors.Is(errStat, os.ErrNotExist) {
			t.Fatalf("new file not rolled back: %v", errStat)
		}
		if info, errStat := root.Lstat("blocked"); errStat != nil || !info.IsDir() {
			t.Fatalf("user directory was not kept: %v, %v", info, errStat)
		}
		return nil
	}); errRoot != nil {
		t.Fatal(errRoot)
	}
}

func TestGitCheckoutPrunePreservesLateFile(t *testing.T) {
	dir := t.TempDir()
	leaf := filepath.Join("parent", "tracked")
	writeSeedTestFile(t, filepath.Join(dir, leaf), "original\n")
	if errRoot := withGitRoot(dir, func(root *os.Root) error {
		before, errRead := readGitCheckoutState(root, leaf)
		if errRead != nil {
			return errRead
		}
		removes := []gitCheckoutUpdate{{name: leaf, before: before}}
		prunes, errPlan := planGitCheckoutPrunes(root, removes, map[string]bool{leaf: true}, nil)
		if errPlan != nil {
			return errPlan
		}
		if len(prunes) != 1 || prunes[0].name != "parent" {
			t.Fatalf("expected to prune parent, got %v", prunes)
		}
		local := filepath.Join(dir, "parent", "local")
		writeSeedTestFile(t, local, "new content\n")
		if errApply := applyGitCheckout(root, append(removes, prunes...), func() error {
			t.Error("checkout finished despite a late addition to a pruned directory")
			return nil
		}); errApply == nil {
			t.Fatal("checkout did not refuse to prune non-empty directory")
		}
		assertLocalFileContents(t, local, "new content\n")
		assertLocalFileContents(t, filepath.Join(dir, leaf), "original\n")
		return nil
	}); errRoot != nil {
		t.Fatal(errRoot)
	}
}

func TestGitCheckoutRollbackPreservesInterveningEdit(t *testing.T) {
	dir := t.TempDir()
	if errRoot := withGitRoot(dir, func(root *os.Root) error {
		updates := []gitCheckoutUpdate{
			{name: "edited", after: &gitCheckoutState{mode: 0o644, data: []byte("remote contents\n")}},
			{name: "untouched", after: &gitCheckoutState{mode: 0o644, data: []byte("remote contents\n")}},
		}
		errFinish := errors.New("config CAS failed")
		const userEdit = "user edit while applying checkout\n"
		errApply := applyGitCheckout(root, updates, func() error {
			writeSeedTestFile(t, filepath.Join(dir, "edited"), userEdit)
			return errFinish
		})
		if !errors.Is(errApply, errFinish) {
			t.Fatalf("checkout error = %v, want %v", errApply, errFinish)
		}
		assertLocalFileContents(t, filepath.Join(dir, "edited"), userEdit)
		if _, errStat := root.Lstat("untouched"); !errors.Is(errStat, os.ErrNotExist) {
			t.Fatalf("untouched file not rolled back: %v", errStat)
		}
		return nil
	}); errRoot != nil {
		t.Fatal(errRoot)
	}
}
