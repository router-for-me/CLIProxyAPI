package misc

import (
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
