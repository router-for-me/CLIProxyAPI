package api

import (
	"testing"
	"time"

	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
)

func TestServerReload_UpdatesRequestLogPolicy(t *testing.T) {
	futureTime := time.Now().Add(24 * time.Hour).Format(time.RFC3339)
	pastTime := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)

	cfg1 := &proxyconfig.Config{
		SDKConfig: proxyconfig.SDKConfig{
			RequestLog:        true,
			RequestLogAPIKeys: []string{"sk-key-1"},
		},
	}
	server := newTestServerWithConfig(t, cfg1)

	fileLogger, ok := server.requestLogger.(*internallogging.FileRequestLogger)
	if !ok {
		t.Fatalf("expected requestLogger to be *FileRequestLogger, got %T", server.requestLogger)
	}

	if !fileLogger.IsAdmitted("sk-key-1") {
		t.Fatal("expected sk-key-1 to be admitted initially")
	}
	if fileLogger.IsAdmitted("sk-key-2") {
		t.Fatal("expected sk-key-2 to be denied initially")
	}

	// 1. Reload with updated allowlist and future until
	cfg2 := *server.cfg
	cfg2.RequestLogAPIKeys = []string{"sk-key-2"}
	cfg2.RequestLogUntil = futureTime
	server.UpdateClients(&cfg2)

	if fileLogger.IsAdmitted("sk-key-1") {
		t.Fatal("expected sk-key-1 to be denied after reload")
	}
	if !fileLogger.IsAdmitted("sk-key-2") {
		t.Fatal("expected sk-key-2 to be admitted after reload")
	}

	// 2. Reload with expired until
	cfg3 := *server.cfg
	cfg3.RequestLogUntil = pastTime
	server.UpdateClients(&cfg3)

	if fileLogger.IsAdmitted("sk-key-2") {
		t.Fatal("expected sk-key-2 to be denied when until is expired")
	}
	if fileLogger.IsEnabled() {
		t.Fatal("expected IsEnabled to be false when until is expired")
	}

	// 3. Reload with request-log disabled
	cfg4 := *server.cfg
	cfg4.RequestLog = false
	cfg4.RequestLogUntil = ""
	server.UpdateClients(&cfg4)

	if fileLogger.IsEnabled() {
		t.Fatal("expected IsEnabled to be false when request-log is false")
	}
	if fileLogger.IsAdmitted("sk-key-2") {
		t.Fatal("expected sk-key-2 to be denied when request-log is false")
	}
}
