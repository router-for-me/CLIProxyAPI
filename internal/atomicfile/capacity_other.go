//go:build !unix && !windows

package atomicfile

// capacityError has no portable error codes to check here.
func capacityError(error) bool { return false }
