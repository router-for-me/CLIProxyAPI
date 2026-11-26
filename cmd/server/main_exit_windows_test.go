package main

import (
	"syscall"

	"golang.org/x/sys/windows"
)

var serverExitProcessAttrs = syscall.SysProcAttr{CreationFlags: windows.DETACHED_PROCESS}
