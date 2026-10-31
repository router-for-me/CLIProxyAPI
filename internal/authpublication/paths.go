package authpublication

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Paths includes credentials whose first local publication failed, so directory
// scans cannot interpret their absent mirror as a credential deletion.
func Paths(dir string) ([]string, error) {
	return discoverPaths(dir, false)
}

// Files discovers credentials before and after their pending mirror is repaired.
func Files(dir string) ([]string, error) {
	return discoverPaths(dir, true)
}

func discoverPaths(dir string, mirrors bool) ([]string, error) {
	paths := make(map[string]struct{})
	err := fs.WalkDir(os.DirFS(dir), ".", func(relative string, entry fs.DirEntry, errWalk error) error {
		path := filepath.Join(dir, filepath.FromSlash(relative))
		if errors.Is(errWalk, os.ErrNotExist) {
			return nil
		}
		if errWalk != nil {
			return errWalk
		}
		if entry.IsDir() {
			if entry.Name() == DirectoryName {
				nested, errRead := pathsInDirectory(filepath.Dir(path))
				if errRead != nil {
					return errRead
				}
				for _, name := range nested {
					paths[name] = struct{}{}
				}
				return filepath.SkipDir
			}
			if relative != "." && (entry.Name() == ".git" || strings.HasPrefix(entry.Name(), ".auth-acquisition-")) {
				return filepath.SkipDir
			}
			return nil
		}
		if mirrors && strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			paths[path] = struct{}{}
		}
		return nil
	})
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	return ordered, err
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
