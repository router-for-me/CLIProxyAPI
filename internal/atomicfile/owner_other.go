//go:build !unix

package atomicfile

import "os"

// linkCount is not available here; a file is treated as having one link.
func linkCount(os.FileInfo) uint64 { return 1 }

// keepOwner has no Unix owner to copy here.
func keepOwner(*os.File, os.FileInfo) error { return nil }
