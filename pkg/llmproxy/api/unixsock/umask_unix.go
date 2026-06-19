//go:build unix

package unixsock

import "syscall"

func clearUmask() {
	syscall.Umask(0)
}
