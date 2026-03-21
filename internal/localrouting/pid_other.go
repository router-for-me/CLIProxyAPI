//go:build !darwin && !linux

package localrouting

func processAlive(pid int) bool {
	return pid > 0
}
