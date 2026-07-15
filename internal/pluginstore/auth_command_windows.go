//go:build windows

package pluginstore

import "os/exec"

func configureAuthCommandCancellation(command *exec.Cmd) {}
