package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
)

func TestConsoleLogWritesToLogger(t *testing.T) {
	var out bytes.Buffer
	logger := log.StandardLogger()
	originalOut := logger.Out
	originalFormatter := logger.Formatter
	originalLevel := logger.Level
	log.SetOutput(&out)
	log.SetFormatter(&log.TextFormatter{
		DisableColors:    true,
		DisableTimestamp: true,
	})
	log.SetLevel(log.InfoLevel)
	defer func() {
		log.SetOutput(originalOut)
		log.SetFormatter(originalFormatter)
		log.SetLevel(originalLevel)
	}()

	engine := newJSEngine()
	_, errRun := engine.vm.RunString(`console.log("alpha", 42, true);`)
	if errRun != nil {
		t.Fatalf("RunString() error = %v", errRun)
	}

	got := out.String()
	if !strings.Contains(got, "JS console log: alpha 42 true") {
		t.Fatalf("console.log output = %q, want logger output with JS message", got)
	}
}

func TestConsoleLogUsesConfiguredLogger(t *testing.T) {
	var messages []string
	engine := newJSEngine(func(message string) error {
		messages = append(messages, message)
		return nil
	})
	_, errRun := engine.vm.RunString(`console.log("alpha", 42, true);`)
	if errRun != nil {
		t.Fatalf("RunString() error = %v", errRun)
	}
	if len(messages) != 1 || messages[0] != "alpha 42 true" {
		t.Fatalf("console log messages = %#v, want formatted message", messages)
	}
}

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
