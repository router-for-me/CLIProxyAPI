package home

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	log "github.com/sirupsen/logrus"
)

func TestHashKeyPart(t *testing.T) {
	first := HashKeyPart("secret-value")
	again := HashKeyPart("secret-value")
	other := HashKeyPart("other-value")
	if first == "" || len(first) != 64 {
		t.Fatalf("HashKeyPart() = %q, want 64 hex chars", first)
	}
	if first != again {
		t.Fatalf("HashKeyPart() is not stable")
	}
	if first == other {
		t.Fatalf("HashKeyPart() returned same hash for different inputs")
	}
	if strings.Contains(first, "secret") || strings.Contains(first, "value") {
		t.Fatalf("HashKeyPart() leaked input: %q", first)
	}
}

func TestKVRequiredHelpersReturnNonHomeMode(t *testing.T) {
	ClearCurrent()
	t.Cleanup(ClearCurrent)

	var out map[string]string
	homeMode, found, errGet := KVGetJSONRequired(context.Background(), "key", &out)
	if errGet != nil {
		t.Fatalf("KVGetJSONRequired() error = %v", errGet)
	}
	if homeMode || found {
		t.Fatalf("KVGetJSONRequired() = homeMode %v found %v, want false false", homeMode, found)
	}
}

func TestCurrentKVClientUnavailableErrors(t *testing.T) {
	t.Cleanup(ClearCurrent)

	disabled := New(config.HomeConfig{Enabled: false})
	SetCurrent(disabled)
	if _, homeMode, errClient := CurrentKVClient(); !homeMode || errClient == nil {
		t.Fatalf("CurrentKVClient(disabled) = homeMode %v err %v, want true error", homeMode, errClient)
	}

	notReady := New(config.HomeConfig{Enabled: true, Host: "127.0.0.1", Port: 1})
	SetCurrent(notReady)
	if _, homeMode, errClient := CurrentKVClient(); !homeMode || errClient == nil {
		t.Fatalf("CurrentKVClient(no heartbeat) = homeMode %v err %v, want true error", homeMode, errClient)
	}
}

func TestKVRequiredHelpersPropagateClientErrors(t *testing.T) {
	client, _ := newRedisCommandTestClient(t, func(args []string) string {
		return "-ERR home kv unavailable\r\n"
	})
	client.heartbeatOK.Store(true)
	SetCurrent(client)
	t.Cleanup(ClearCurrent)

	var out map[string]string
	homeMode, _, errGet := KVGetJSONRequired(context.Background(), "cpa:test:key", &out)
	if !homeMode || errGet == nil {
		t.Fatalf("KVGetJSONRequired() = homeMode %v err %v, want true error", homeMode, errGet)
	}
	homeMode, errSet := KVSetJSONRequired(context.Background(), "cpa:test:key", map[string]string{"value": "secret"}, 0)
	if !homeMode || errSet == nil {
		t.Fatalf("KVSetJSONRequired() = homeMode %v err %v, want true error", homeMode, errSet)
	}
}

func TestKVBestEffortWriteSwallowsErrorAndRedactsLog(t *testing.T) {
	client, _ := newRedisCommandTestClient(t, func(args []string) string {
		return "-ERR home kv unavailable\r\n"
	})
	client.heartbeatOK.Store(true)
	SetCurrent(client)
	t.Cleanup(ClearCurrent)

	logger := log.StandardLogger()
	previousOutput := logger.Out
	previousLevel := log.GetLevel()
	buffer := &bytes.Buffer{}
	log.SetOutput(buffer)
	log.SetLevel(log.ErrorLevel)
	t.Cleanup(func() {
		log.SetOutput(previousOutput)
		log.SetLevel(previousLevel)
	})

	ok := KVSetJSONBestEffort(context.Background(), "cpa:test:secret-key", map[string]string{"value": "secret-value"}, 0)
	if ok {
		t.Fatalf("KVSetJSONBestEffort() = true, want false")
	}
	logText := buffer.String()
	if !strings.Contains(logText, "cpa:test:*") {
		t.Fatalf("log = %q, want redacted key prefix", logText)
	}
	if strings.Contains(logText, "secret-key") || strings.Contains(logText, "secret-value") {
		t.Fatalf("log leaked key or value: %q", logText)
	}
}
