package main

import (
	"testing"
	"time"
)

func TestStopInterruptTimerClearsExpiredInterrupt(t *testing.T) {
	engine := newJSEngine()
	timer, done := engine.startInterruptTimer(time.Nanosecond)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("interrupt timer did not fire")
	}

	engine.stopInterruptTimer(timer, done)
	value, errRun := engine.vm.RunString("1 + 1")
	if errRun != nil {
		t.Fatalf("RunString() error after clearing interrupt = %v", errRun)
	}
	if got := value.ToInteger(); got != 2 {
		t.Fatalf("RunString() = %d, want 2", got)
	}
}
