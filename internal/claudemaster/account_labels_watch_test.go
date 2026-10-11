package claudemaster

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

const watchTestAccount = "11111111-2222-3333-4444-555555555555"

func watchTestKey() string {
	return clientAccountKey([]byte(`{"metadata":{"user_id":"{\"account_uuid\":\"` + watchTestAccount + `\"}"}}`))
}

func TestAccountLabelsReloadWhenTheFileChanges(t *testing.T) {
	stop, err := StartTelemetry(TelemetryOptions{Reader: sdkmetric.NewManualReader(), Instance: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stop(context.Background()) }()
	if got := watchTestKey(); got == "alice" {
		t.Fatalf("labelled before any labels: %s", got)
	}
	file := filepath.Join(t.TempDir(), "labels")
	if err := os.WriteFile(file, []byte("# none yet\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	var mu sync.Mutex
	label, fail := "alice", false
	set := func(l string, f bool) { mu.Lock(); label, fail = l, f; mu.Unlock() }
	start := accountLabelsFingerprint(file) // before the goroutine: the first replace below must read as a change
	go func() {
		defer close(done)
		watchAccountLabelsFrom(ctx, file, start, 10*time.Millisecond, func() (map[string]string, error) {
			mu.Lock()
			defer mu.Unlock()
			if fail {
				return nil, errors.New("an account label must be ACCOUNT_UUID=NAME")
			}
			return map[string]string{watchTestAccount: label}, nil
		})
	}()
	replace := func(body string) {
		tmp := file + ".new"
		if err := os.WriteFile(tmp, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, file); err != nil {
			t.Fatal(err)
		}
	}
	waitFor := func(want string) {
		deadline := time.Now().Add(5 * time.Second)
		for watchTestKey() != want {
			if time.Now().After(deadline) {
				t.Fatalf("account key = %s, want %s", watchTestKey(), want)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	replace(watchTestAccount + "=alice\n")
	waitFor("alice")

	// A file that does not load keeps the labels in use.
	set("alice", true)
	replace("not a label line\n")
	time.Sleep(100 * time.Millisecond)
	if got := watchTestKey(); got != "alice" {
		t.Fatalf("a bad file replaced the labels: %s", got)
	}
	set("bob", false)
	replace(watchTestAccount + "=bob\n")
	waitFor("bob")
	cancel()
	<-done
}

func TestSetAccountLabelsWithoutTelemetryIsANoop(t *testing.T) {
	if SetAccountLabels(map[string]string{watchTestAccount: "x"}) {
		t.Fatal("labels applied with no telemetry running")
	}
}
