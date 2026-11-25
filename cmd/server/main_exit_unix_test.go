//go:build unix

package main

import "syscall"

var serverExitProcessAttrs = syscall.SysProcAttr{Setsid: true}
