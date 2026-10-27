// Package authpublication keeps remote commits authoritative while their local
// credential mirror cannot be replaced.
package authpublication

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const DirectoryName = ".auth-publications"

var ErrPending = errors.New("credential publication awaits remote reconciliation")

type record struct {
	Name      string `json:"name"`
	Data      []byte `json:"data"`
	Committed bool   `json:"committed"`
}

type Pending struct {
	path     string
	record   record
	previous []byte
	finished bool
}

func markerPath(path string) string {
	sum := sha256.Sum256([]byte(filepath.Base(path)))
	return filepath.Join(filepath.Dir(path), DirectoryName, hex.EncodeToString(sum[:])+".json")
}

// Prepare fences readers before the remote write. A crash before confirmation
// leaves an incomplete observation, never an activated uncommitted credential.
func Prepare(path string, data []byte) (*Pending, error) {
	marker := markerPath(path)
	previous, err := os.ReadFile(marker)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	p := &Pending{path: path, record: record{Name: filepath.Base(path), Data: data}, previous: previous}
	return p, writeRecord(marker, p.record)
}

func (p *Pending) Commit() error {
	p.record.Committed = true
	if err := writeRecord(markerPath(p.path), p.record); err != nil {
		// Keep the prepared barrier if confirmation cannot be written. Runtime
		// notification can still use the already committed in-memory credential.
		p.finished = true
		return err
	}
	p.finished = true
	return nil
}

func (p *Pending) Abort() error {
	if p == nil || p.finished {
		return nil
	}
	p.finished = true
	if len(p.previous) == 0 {
		return Clear(p.path)
	}
	return writeBytes(markerPath(p.path), p.previous)
}

func load(path string) (record, bool, error) {
	var r record
	data, err := os.ReadFile(markerPath(path))
	if errors.Is(err, os.ErrNotExist) {
		return r, false, nil
	}
	if err != nil {
		return r, true, err
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return r, true, err
	}
	if r.Name != filepath.Base(path) || len(r.Data) == 0 {
		return r, true, fmt.Errorf("invalid credential publication record")
	}
	return r, true, nil
}

func HasPending(path string) bool {
	_, exists, _ := load(path)
	return exists
}

// ReadFile includes a confirmed remote commit even while the old JSON remains
// readable. Unconfirmed records keep scans incomplete until backend recovery.
func ReadFile(path string) ([]byte, error) {
	r, exists, err := load(path)
	if err != nil {
		return nil, err
	}
	if !exists {
		return os.ReadFile(path)
	}
	if !r.Committed {
		return nil, ErrPending
	}
	return r.Data, nil
}

// Paths includes credentials whose first local publication failed, so directory
// scans cannot interpret their absent mirror as a credential deletion.
func Paths(dir string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, errWalk error) error {
		if errors.Is(errWalk, os.ErrNotExist) {
			return nil
		}
		if errWalk != nil {
			return errWalk
		}
		if !entry.IsDir() || entry.Name() != DirectoryName {
			return nil
		}
		nested, errRead := pathsInDirectory(filepath.Dir(path))
		if errRead != nil {
			return errRead
		}
		paths = append(paths, nested...)
		return filepath.SkipDir
	})
	return paths, err
}

func pathsInDirectory(dir string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(dir, DirectoryName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, DirectoryName, entry.Name()))
		if err != nil {
			return nil, err
		}
		var r record
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, err
		}
		if r.Name == "." || r.Name == ".." || filepath.Base(r.Name) != r.Name || len(r.Data) == 0 {
			return nil, fmt.Errorf("invalid credential publication path")
		}
		path := filepath.Join(dir, r.Name)
		if markerPath(path) != filepath.Join(dir, DirectoryName, entry.Name()) {
			return nil, fmt.Errorf("credential publication record does not match its path")
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func Clear(path string) error {
	marker := markerPath(path)
	err := os.Remove(marker)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	// Remove only an empty record directory; other credentials may still own it.
	_ = os.Remove(filepath.Dir(marker))
	return nil
}

// Repair runs before another local save can merge or publish stale mirror data.
func Repair(path string) error {
	if !HasPending(path) {
		return nil
	}
	data, err := ReadFile(path)
	if err != nil {
		return err
	}
	if err := writeBytes(path, data); err != nil {
		return err
	}
	return Clear(path)
}

func writeRecord(path string, r record) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return writeBytes(path, data)
}

func writeBytes(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".publication-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

// ReconciledDirectory retires publication records after startup has loaded an
// authoritative backend snapshot. Callers must exclude concurrent saves.
func ReconciledDirectory(dir string) error {
	return filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if entry.IsDir() && entry.Name() == DirectoryName {
			if err := os.RemoveAll(path); err != nil {
				return err
			}
			return filepath.SkipDir
		}
		return nil
	})
}

func DiscardUnconfirmed(path string) error {
	r, exists, err := load(path)
	if err != nil || !exists || r.Committed {
		return err
	}
	return Clear(path)
}

func Stat(path string) (os.FileInfo, error) {
	if HasPending(path) {
		return os.Stat(markerPath(path))
	}
	return os.Stat(path)
}
