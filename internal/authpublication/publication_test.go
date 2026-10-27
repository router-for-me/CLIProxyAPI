package authpublication

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestAuthPublicationSurvivesRescanAndRepairsMirror(t *testing.T) {
	for _, existing := range []bool{false, true} {
		name := "new"
		if existing {
			name = "replacement"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "account.json")
			if existing {
				if err := os.WriteFile(path, []byte(`{"access_token":"old"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			data := []byte(`{"access_token":"committed"}`)
			pending, err := Prepare(path, data)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ReadFile(path); !errors.Is(err, ErrPending) {
				t.Fatalf("unconfirmed credential became readable: %v", err)
			}
			if err := pending.Commit(); err != nil {
				t.Fatal(err)
			}
			// Drop the writer. A fresh reader has only the files left on disk.
			pending = nil
			got, err := ReadFile(path)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("fresh reader lost committed credential: %q, %v", got, err)
			}
			paths, err := Paths(dir)
			if err != nil || len(paths) != 1 || paths[0] != path {
				t.Fatalf("scan lost pending credential: %v, %v", paths, err)
			}
			if err := Repair(path); err != nil {
				t.Fatal(err)
			}
			got, err = os.ReadFile(path)
			if err != nil || !bytes.Equal(got, data) || HasPending(path) {
				t.Fatalf("mirror repair did not retire the overlay: %q, %v", got, err)
			}
		})
	}
}

func TestAuthPublicationAbortPreservesPreviousCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "account.json")
	first := []byte(`{"access_token":"committed"}`)
	pending, err := Prepare(path, first)
	if err != nil {
		t.Fatal(err)
	}
	if err := pending.Commit(); err != nil {
		t.Fatal(err)
	}
	failed, err := Prepare(path, []byte(`{"access_token":"rejected"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := failed.Abort(); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(path)
	if err != nil || !bytes.Equal(got, first) {
		t.Fatalf("failed replacement lost the last committed credential: %q, %v", got, err)
	}
	if err := os.WriteFile(path, []byte(`{"access_token":"newer-remote"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ReconciledDirectory(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	got, err = ReadFile(path)
	if err != nil || !bytes.Contains(got, []byte("newer-remote")) || HasPending(path) {
		t.Fatalf("backend reconciliation kept an obsolete overlay: %q, %v", got, err)
	}
}
