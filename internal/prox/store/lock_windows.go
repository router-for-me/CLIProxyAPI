//go:build windows

package store

import "os"

// Windows has no flock; a single proxy process per state directory is assumed.
func lockFile(*os.File) error { return nil }

func unlockFile(*os.File) {}

func syncDir(string) {}
