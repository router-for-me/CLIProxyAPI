package auth

import (
	"testing"
	"time"
)

func TestRefreshDeadlineTimerResetAndStop(t *testing.T) {
	timer := newRefreshDeadlineTimer(time.Hour)
	defer timer.Close()

	timer.Stop()
	timer.Reset(10 * time.Millisecond)
	select {
	case <-timer.Chan():
	case <-time.After(time.Second):
		t.Fatal("refresh deadline timer did not fire after reset")
	}

	timer.Reset(100 * time.Millisecond)
	timer.Stop()
	select {
	case <-timer.Chan():
		t.Fatal("refresh deadline timer fired after stop")
	case <-time.After(25 * time.Millisecond):
	}
}
