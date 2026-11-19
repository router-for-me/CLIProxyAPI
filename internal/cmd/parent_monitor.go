package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var errParentProcessExited = errors.New("parent process exited")

type processExistsFunc func(int) (bool, error)

func validateParentPID(pid int, processExists processExistsFunc) error {
	if pid <= 0 {
		return fmt.Errorf("parent pid must be positive")
	}
	if uint64(pid) > maxProcessID {
		return fmt.Errorf("parent pid exceeds the supported process id range")
	}
	exists, err := processExists(pid)
	if err != nil {
		return fmt.Errorf("check parent process %d: %w", pid, err)
	}
	if !exists {
		return fmt.Errorf("parent process %d: %w", pid, errParentProcessExited)
	}
	return nil
}

func monitorParent(ctx context.Context, pid int, interval time.Duration, processExists processExistsFunc) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if interval <= 0 {
		return fmt.Errorf("parent monitor interval must be positive")
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := validateParentPID(pid, processExists); err != nil {
			return err
		}

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}
