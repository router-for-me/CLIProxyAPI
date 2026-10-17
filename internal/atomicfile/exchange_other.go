//go:build !linux && !darwin

package atomicfile

import "errors"

var errExchangeUnsupported = errors.New("atomicfile: exchange rename not supported")

// exchangeFiles has no atomic implementation here; callers fall back to a plain rename.
func exchangeFiles(string, string) error { return errExchangeUnsupported }

func exchangeUnsupported(err error) bool { return errors.Is(err, errExchangeUnsupported) }

func exchangeSupported() bool { return false }
