//go:build !unix

package atomicfile

import (
	"errors"
	"os"
)

// linkCount is not available here; a file is treated as having one link.
func linkCount(os.FileInfo) uint64 { return 1 }

// keepOwner has no Unix owner to copy here.
func keepOwner(*os.File, os.FileInfo) error { return nil }

// syncUnsupported reports an fsync error meaning the file cannot be synced at all.
func syncUnsupported(err error) bool { return errors.Is(err, errors.ErrUnsupported) }
