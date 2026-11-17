//go:build !linux && !darwin

package selfupdate

import "errors"

const platformSupported = false

var errUnsupportedPlatform = errors.New("in-place update is supported on linux and macOS only")

func dupListenerFD(*gateListener) (int, error) { return -1, errUnsupportedPlatform }

func clearCloexec(int) error { return errUnsupportedPlatform }

func closeFD(int) {}

func execBinary(string, []string, []string) error { return errUnsupportedPlatform }

func isWritableDir(string) error { return errUnsupportedPlatform }
