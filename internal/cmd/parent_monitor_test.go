package cmd

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestValidateParentPIDRejectsPlatformOverflow(t *testing.T) {
	if uint64(^uint(0)>>1) <= maxProcessID {
		t.Skip("int cannot represent a value larger than the platform process id")
	}
	checked := false
	pid := int(maxProcessID + 1)
	err := validateParentPID(pid, func(int) (bool, error) {
		checked = true
		return true, nil
	})
	if err == nil {
		t.Fatal("validateParentPID() error = nil, want uint32 range error")
	}
	if checked {
		t.Fatal("process existence check ran for out-of-range pid")
	}
}

func TestValidateParentPIDRejectsMissingProcess(t *testing.T) {
	err := validateParentPID(4321, func(pid int) (bool, error) {
		if pid != 4321 {
			t.Fatalf("pid = %d, want 4321", pid)
		}
		return false, nil
	})
	if !errors.Is(err, errParentProcessExited) {
		t.Fatalf("error = %v, want errParentProcessExited", err)
	}
}

func TestMonitorParentReturnsWhenProcessExits(t *testing.T) {
	checks := 0
	err := monitorParent(context.Background(), 4321, time.Millisecond, func(pid int) (bool, error) {
		checks++
		return checks < 2, nil
	})
	if !errors.Is(err, errParentProcessExited) {
		t.Fatalf("error = %v, want errParentProcessExited", err)
	}
	if checks != 2 {
		t.Fatalf("checks = %d, want 2", checks)
	}
}

func TestMonitorParentStopsWithContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := monitorParent(ctx, 4321, time.Hour, func(int) (bool, error) {
		t.Fatal("process check should not run after cancellation")
		return true, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
