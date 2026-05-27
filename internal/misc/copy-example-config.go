package misc

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	log "github.com/sirupsen/logrus"
)

func CopyConfigTemplate(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		if errClose := in.Close(); errClose != nil {
			log.WithError(errClose).Warn("failed to close source config file")
		}
	}()

	if err = os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if errClose := out.Close(); errClose != nil {
			log.WithError(errClose).Warn("failed to close destination config file")
		}
	}()

	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// FindExampleConfig locates config.example.yaml in the given directory.
// Returns the absolute path or an error if the file is missing or is a directory.
func FindExampleConfig(wd string) (string, error) {
	path := filepath.Join(wd, "config.example.yaml")
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("config.example.yaml not found in %s; place it alongside the binary or working directory", wd)
	}
	if info.IsDir() {
		return "", fmt.Errorf("config.example.yaml in %s is a directory, not a file", wd)
	}
	return path, nil
}
