//go:build linux

package auth

import (
	"testing"
	"time"
)

func TestRefreshDeadlineTimerUsesBootTimeOnLinux(t *testing.T) {
	timer := newRefreshDeadlineTimer(time.Hour)
	defer timer.Close()
	if _, ok := timer.(*linuxBootTimeTimer); !ok {
		t.Fatalf("newRefreshDeadlineTimer() = %T, want CLOCK_BOOTTIME implementation", timer)
	}
}
