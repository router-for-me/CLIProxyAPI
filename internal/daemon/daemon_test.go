package daemon

import (
	"bufio"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDaemonHelperProcess(t *testing.T) {
	if !IsChild() {
		return
	}
	manager, errManager := DefaultManager()
	if errManager != nil {
		t.Fatalf("DefaultManager() error = %v", errManager)
	}
	registration, errRegister := manager.RegisterChild()
	if errRegister != nil {
		t.Fatalf("RegisterChild() error = %v", errRegister)
	}
	<-registration.Context().Done()
	if errClose := registration.Close(); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}
}

func TestManagerStartAndStopBackgroundProcess(t *testing.T) {
	tempDir := t.TempDir()
	manager := newManager(filepath.Join(tempDir, "state"), filepath.Join(tempDir, "logs", "daemon.log"))
	manager.startWait = 5 * time.Second
	manager.stopWait = 5 * time.Second
	manager.pollInterval = 10 * time.Millisecond

	state, errStart := manager.Start([]string{"-test.run=^TestDaemonHelperProcess$", "-d"})
	if errStart != nil {
		t.Fatalf("Start() error = %v", errStart)
	}
	if state.PID <= 1 {
		t.Fatalf("Start() pid = %d", state.PID)
	}
	if state.ControlToken == "" {
		t.Fatal("Start() returned an empty control token")
	}

	result, errStop := manager.Stop()
	if errStop != nil {
		t.Fatalf("Stop() error = %v", errStop)
	}
	if !result.WasRunning || result.PID != state.PID {
		t.Fatalf("Stop() result = %+v, want running pid %d", result, state.PID)
	}
	if _, errStat := os.Stat(manager.statePath); !os.IsNotExist(errStat) {
		t.Fatalf("state file still exists: %v", errStat)
	}
}

func TestRegistrationRejectsInvalidControlToken(t *testing.T) {
	tempDir := t.TempDir()
	manager := newManager(filepath.Join(tempDir, "state"), filepath.Join(tempDir, "daemon.log"))
	registration, errRegister := manager.RegisterChild()
	if errRegister != nil {
		t.Fatalf("RegisterChild() error = %v", errRegister)
	}
	defer func() {
		if errClose := registration.Close(); errClose != nil {
			t.Errorf("Close() error = %v", errClose)
		}
	}()

	connection, errDial := net.Dial("tcp", registration.state.ControlAddr)
	if errDial != nil {
		t.Fatalf("Dial() error = %v", errDial)
	}
	if _, errWrite := connection.Write([]byte("wrong-token\n")); errWrite != nil {
		t.Fatalf("Write() error = %v", errWrite)
	}
	response, errRead := bufio.NewReader(connection).ReadString('\n')
	_ = connection.Close()
	if errRead != nil {
		t.Fatalf("ReadString() error = %v", errRead)
	}
	if response != "denied\n" {
		t.Fatalf("response = %q, want denied", response)
	}

	select {
	case <-registration.Context().Done():
		t.Fatal("invalid token cancelled the daemon context")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestStripDaemonFlag(t *testing.T) {
	got := stripDaemonFlag([]string{"--config", "config.yaml", "-d", "--local-model", "--d=true"})
	want := []string{"--config", "config.yaml", "--local-model"}
	if len(got) != len(want) {
		t.Fatalf("stripDaemonFlag() = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("stripDaemonFlag() = %v, want %v", got, want)
		}
	}
}
