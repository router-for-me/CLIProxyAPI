package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"gopkg.in/yaml.v3"
)

type requestLoggerPolicyRecorder struct {
	logging.RequestLogger
	keys  []string
	names []string
	noLog []string
}

func (r *requestLoggerPolicyRecorder) SetAPIKeyNames(keys, names []string) {
	r.keys = append([]string(nil), keys...)
	r.names = append([]string(nil), names...)
}

func (r *requestLoggerPolicyRecorder) SetNoLogAPIKeys(keys []string) {
	r.noLog = append([]string(nil), keys...)
}

type requestLoggerWithoutPolicySupport struct {
	logging.RequestLogger
}

func newRequestLogPolicyReloadTestServer(t *testing.T, oldNoLog []string) (*Server, config.Config) {
	t.Helper()
	server := newTestServerWithOptions(t, WithRequestLoggerFactory(
		func(cfg *config.Config, configPath string) logging.RequestLogger {
			return &requestLoggerWithoutPolicySupport{
				RequestLogger: defaultRequestLoggerFactory(cfg, configPath),
			}
		},
	))
	if server.requestLogPolicy == nil {
		t.Fatal("server requestLogPolicy is nil")
	}
	if _, ok := server.requestLogger.(interface{ ShouldSkipLog(string) bool }); ok {
		t.Fatal("test logger unexpectedly exposes no-log capability")
	}
	oldCfg := *server.cfg
	oldCfg.APIKeysNoLog = append([]string(nil), oldNoLog...)
	server.cfg = &oldCfg
	server.requestLogPolicy.SetNoLogAPIKeys(oldNoLog)
	snapshot, errMarshal := yaml.Marshal(&oldCfg)
	if errMarshal != nil {
		t.Fatalf("marshal old config: %v", errMarshal)
	}
	server.oldConfigYaml = snapshot
	return server, oldCfg
}

func TestDefaultRequestLoggerFactoryAppliesAPIKeyLoggingPolicies(t *testing.T) {
	cfg := &config.Config{SDKConfig: config.SDKConfig{
		RequestLog:   true,
		APIKeys:      []string{"key-a", "key-b"},
		APIKeyNames:  []string{"Alice", "Bob"},
		APIKeysNoLog: []string{"key-b"},
	}}
	logger := defaultRequestLoggerFactory(cfg, filepath.Join(t.TempDir(), "config.yaml"))
	checker, ok := logger.(interface{ ShouldSkipLog(string) bool })
	if !ok || !checker.ShouldSkipLog("key-b") || checker.ShouldSkipLog("key-a") {
		t.Fatal("default logger did not apply API-key logging policies")
	}
}

func TestApplyAPIKeyLoggingPoliciesRefreshesLoggerMirror(t *testing.T) {
	recorder := &requestLoggerPolicyRecorder{}
	cfg := &config.Config{SDKConfig: config.SDKConfig{
		APIKeys:      []string{"key-b"},
		APIKeyNames:  []string{"Bob"},
		APIKeysNoLog: []string{"key-b"},
	}}
	applyAPIKeyLoggingPolicies(recorder, cfg)
	if !reflect.DeepEqual(recorder.keys, cfg.APIKeys) ||
		!reflect.DeepEqual(recorder.names, cfg.APIKeyNames) ||
		!reflect.DeepEqual(recorder.noLog, cfg.APIKeysNoLog) {
		t.Fatalf("policies = keys:%v names:%v no-log:%v", recorder.keys, recorder.names, recorder.noLog)
	}
}

func TestUpdateClientsContextCommitsServerOwnedNoLogPolicy(t *testing.T) {
	server, oldCfg := newRequestLogPolicyReloadTestServer(t, []string{"old-key"})
	nextCfg := oldCfg
	nextCfg.APIKeysNoLog = []string{"new-key"}
	if !server.UpdateClientsContext(context.Background(), &nextCfg) {
		t.Fatal("UpdateClientsContext() = false, want true")
	}
	if server.requestLogPolicy.ShouldSkipLog("old-key") || !server.requestLogPolicy.ShouldSkipLog("new-key") {
		t.Fatal("successful reload did not commit the candidate no-log set")
	}
}

func TestUpdateClientsContextRetainsServerOwnedNoLogUnionOnFailure(t *testing.T) {
	server, oldCfg := newRequestLogPolicyReloadTestServer(t, []string{"old-key"})
	nextCfg := oldCfg
	nextCfg.APIKeysNoLog = []string{"new-key"}
	nextCfg.WebsocketAuth = !oldCfg.WebsocketAuth
	ctx, cancel := context.WithCancel(context.Background())
	sawUnion := false
	server.SetWebsocketAuthChangeHandler(func(_, _ bool) {
		sawUnion = server.requestLogPolicy.ShouldSkipLog("old-key") &&
			server.requestLogPolicy.ShouldSkipLog("new-key")
		cancel()
	})
	if server.UpdateClientsContext(ctx, &nextCfg) {
		t.Fatal("UpdateClientsContext() = true after forced cancellation")
	}
	if !sawUnion {
		t.Fatal("in-flight reload did not publish old and candidate no-log keys")
	}
	if !server.requestLogPolicy.ShouldSkipLog("old-key") || !server.requestLogPolicy.ShouldSkipLog("new-key") {
		t.Fatal("failed reload did not retain the old and candidate no-log union")
	}
}

func TestUpdateClientsContextFailureKeepsCandidateAuthorizedRequestFailClosed(t *testing.T) {
	logsDir := filepath.Join(t.TempDir(), "request-logs")
	if errMkdir := os.MkdirAll(logsDir, 0o750); errMkdir != nil {
		t.Fatalf("create request log directory: %v", errMkdir)
	}
	server := newTestServerWithOptions(t, WithRequestLoggerFactory(
		func(*config.Config, string) logging.RequestLogger {
			return &requestLoggerWithoutPolicySupport{
				RequestLogger: logging.NewFileRequestLogger(true, logsDir, "", 10),
			}
		},
	))
	if _, ok := server.requestLogger.(interface{ ShouldSkipLog(string) bool }); ok {
		t.Fatal("test logger unexpectedly exposes no-log capability")
	}

	const oldKey = "test-key"
	const candidateKey = "candidate-no-log-key"
	oldCfg := *server.cfg
	oldCfg.RequestLog = true
	oldCfg.APIKeys = []string{oldKey}
	oldCfg.APIKeysNoLog = []string{oldKey}
	server.cfg = &oldCfg
	server.requestLogPolicy.SetNoLogAPIKeys(oldCfg.APIKeysNoLog)
	snapshot, errMarshal := yaml.Marshal(&oldCfg)
	if errMarshal != nil {
		t.Fatalf("marshal old config: %v", errMarshal)
	}
	server.oldConfigYaml = snapshot

	server.engine.POST("/privacy-reload-probe", AuthMiddleware(server.accessManager), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	nextCfg := oldCfg
	nextCfg.APIKeys = []string{oldKey, candidateKey}
	nextCfg.APIKeysNoLog = []string{oldKey, candidateKey}
	nextCfg.WebsocketAuth = !oldCfg.WebsocketAuth
	t.Cleanup(func() {
		server.applyAccessConfig(&nextCfg, &oldCfg)
	})

	ctx, cancel := context.WithCancel(context.Background())
	sawCandidateConfig := false
	server.SetWebsocketAuthChangeHandler(func(_, _ bool) {
		sawCandidateConfig = server.cfg == &nextCfg
		cancel()
	})
	if server.UpdateClientsContext(ctx, &nextCfg) {
		t.Fatal("UpdateClientsContext() = true after forced cancellation")
	}
	if !sawCandidateConfig {
		t.Fatal("forced cancellation did not occur after candidate config publication")
	}

	authRequest := httptest.NewRequest(http.MethodPost, "/privacy-reload-probe", nil)
	authRequest.Header.Set("Authorization", "Bearer "+candidateKey)
	authResult, authErr := server.accessManager.Authenticate(context.Background(), authRequest)
	if authErr != nil {
		t.Fatalf("candidate key authentication failed after partial reload: %v", authErr)
	}
	if authResult == nil || authResult.Principal != candidateKey {
		t.Fatalf("candidate key authentication result = %+v", authResult)
	}

	request := httptest.NewRequest(
		http.MethodPost,
		"/privacy-reload-probe",
		strings.NewReader(`{"probe":true}`),
	)
	request.Header.Set("Authorization", "Bearer "+candidateKey)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.engine.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("candidate request status = %d, want %d body=%s", response.Code, http.StatusOK, response.Body.String())
	}

	entries, errReadDir := os.ReadDir(logsDir)
	if errReadDir != nil {
		t.Fatalf("read request log directory: %v", errReadDir)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Errorf("candidate no-log request created artifacts: %v", names)
	}
	if !server.requestLogPolicy.ShouldSkipLog(candidateKey) {
		t.Error("server-owned policy stopped protecting the partially applied candidate key")
	}
}
