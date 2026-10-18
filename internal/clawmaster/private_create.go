package clawmaster

import (
	"crypto/rand"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// createPrivateTemp creates a new file in dir, named prefix and a random suffix, private to this user
// from its creation (createPrivateFile), as os.CreateTemp does with mode 0600.
func createPrivateTemp(dir, prefix string) (*os.File, error) {
	for range 100 {
		f, err := createPrivateFile(filepath.Join(dir, prefix+rand.Text()[:12]))
		if !errors.Is(err, fs.ErrExist) {
			return f, err
		}
	}
	return nil, &fs.PathError{Op: "createtemp", Path: filepath.Join(dir, prefix+"*"), Err: fs.ErrExist}
}
