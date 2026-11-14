package cliproxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/api"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

const issue6631ManagementKey = "issue-6631-management-key"

type issue6631Values struct {
	Debug               bool
	RequestRetry        int
	MaxRetryCredentials int
}

func issue6631ValuesOf(cfg *config.Config) issue6631Values {
	if cfg == nil {
		return issue6631Values{}
	}
	return issue6631Values{Debug: cfg.Debug, RequestRetry: cfg.RequestRetry, MaxRetryCredentials: cfg.MaxRetryCredentials}
}

type issue6631Harness struct {
	t              *testing.T
	service        *Service
	handler        http.Handler
	configPath     string
	firstRead      chan issue6631Values
	releaseFirst   func()
	firstPublished chan struct{}
	published      chan issue6631Values
}

// newIssue6631Harness wires the real Service, watcher reload, API server, and
// management handler. The first reload that reaches the server is paused after
// it read config.yaml, so a test can land a management save in between.
func newIssue6631Harness(t *testing.T) *issue6631Harness {
	t.Helper()
	previousLevel := log.GetLevel()
	t.Cleanup(func() { log.SetLevel(previousLevel) })

	dir := t.TempDir()
	authDir := filepath.Join(dir, "auths")
	if errMkdir := os.MkdirAll(authDir, 0o700); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	hashedKey, errHash := bcrypt.GenerateFromPassword([]byte(issue6631ManagementKey), bcrypt.MinCost)
	if errHash != nil {
		t.Fatal(errHash)
	}
	configPath := filepath.Join(dir, "config.yaml")
	raw := fmt.Sprintf("port: 8317\nauth-dir: %q\ndebug: false\nrequest-retry: 3\nmax-retry-credentials: 0\nremote-management:\n  allow-remote: true\n  secret-key: %q\n", filepath.ToSlash(authDir), string(hashedKey))
	if errWrite := os.WriteFile(configPath, []byte(raw), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := internalconfig.LoadConfig(configPath)
	if errLoad != nil {
		t.Fatal(errLoad)
	}

	service, errBuild := NewBuilder().WithConfig(cfg).WithConfigPath(configPath).Build()
	if errBuild != nil {
		t.Fatal(errBuild)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	watcherWrapper, errWatcher := service.watcherFactory(configPath, authDir, func(newCfg *config.Config) {
		service.applyWatcherConfigUpdate(newCfg)
	})
	if errWatcher != nil {
		t.Fatal(errWatcher)
	}
	service.setWatcher(watcherWrapper)
	service.ensureAuthUpdateQueue(ctx)
	watcherWrapper.SetAuthUpdateQueue(service.authUpdates)
	watcherWrapper.SetConfig(service.currentConfig())
	server := api.NewServer(service.currentConfig(), service.coreManager, service.accessManager, configPath, service.serverOptions...)
	if server == nil {
		t.Fatal("NewServer returned nil")
	}
	service.server = server
	t.Cleanup(func() {
		if service.authQueueStop != nil {
			service.authQueueStop()
		}
		_ = watcherWrapper.Stop()
	})

	releaseCh := make(chan struct{})
	h := &issue6631Harness{
		t:              t,
		service:        service,
		handler:        server.Handler(),
		configPath:     configPath,
		firstRead:      make(chan issue6631Values, 1),
		releaseFirst:   sync.OnceFunc(func() { close(releaseCh) }),
		firstPublished: make(chan struct{}),
		published:      make(chan issue6631Values, 64),
	}
	t.Cleanup(h.releaseFirst)
	var firstOnce sync.Once
	service.updateServerClientsContextFn = func(ctx context.Context, cfg *config.Config) bool {
		first := false
		firstOnce.Do(func() { first = true })
		if first {
			h.firstRead <- issue6631ValuesOf(cfg)
			<-releaseCh
		}
		ok := server.UpdateClientsContext(ctx, cfg)
		if first {
			close(h.firstPublished)
		}
		select {
		case h.published <- issue6631ValuesOf(cfg):
		default:
		}
		return ok
	}
	return h
}

func (h *issue6631Harness) put(path, value string) {
	h.t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPut, "/v0/management/"+path, strings.NewReader(`{"value":`+value+`}`))
	request.Header.Set("Authorization", "Bearer "+issue6631ManagementKey)
	request.Header.Set("Content-Type", "application/json")
	h.handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		h.t.Fatalf("PUT %s: status=%d body=%s", path, recorder.Code, recorder.Body.String())
	}
}

// awaitFirstReload waits until the reload for the first save has read config.yaml.
func (h *issue6631Harness) awaitFirstReload() issue6631Values {
	h.t.Helper()
	select {
	case values := <-h.firstRead:
		return values
	case <-time.After(10 * time.Second):
		h.t.Fatal("reload for the first management save did not start")
	}
	return issue6631Values{}
}

// publishFirstReload lets the paused reload publish what it read and waits for it.
func (h *issue6631Harness) publishFirstReload() {
	h.t.Helper()
	h.releaseFirst()
	select {
	case <-h.firstPublished:
	case <-time.After(10 * time.Second):
		h.t.Fatal("paused reload did not finish publishing")
	}
}

func (h *issue6631Harness) disk() issue6631Values {
	h.t.Helper()
	data, errRead := os.ReadFile(h.configPath)
	if errRead != nil {
		h.t.Fatal(errRead)
	}
	cfg, errParse := internalconfig.ParseConfigBytes(data)
	if errParse != nil {
		h.t.Fatal(errParse)
	}
	return issue6631ValuesOf(cfg)
}

// waitForPublished waits until a post-save reload has published want to the server.
// The timeout only guards against a reload that never comes.
func (h *issue6631Harness) waitForPublished(want issue6631Values) bool {
	timeout := time.After(10 * time.Second)
	for {
		select {
		case got := <-h.published:
			if got == want {
				return true
			}
		case <-timeout:
			return false
		}
	}
}

func TestManagementSequentialWritesSurviveStaleReload_Issue6631(t *testing.T) {
	h := newIssue6631Harness(t)

	h.put("debug", "true")
	if read := h.awaitFirstReload(); read != (issue6631Values{Debug: true, RequestRetry: 3}) {
		t.Fatalf("first reload read %+v, want the config saved by the first write", read)
	}
	h.put("request-retry", "5")
	// The reload that read config.yaml before the second save publishes now.
	h.publishFirstReload()
	h.put("max-retry-credentials", "7")

	want := issue6631Values{Debug: true, RequestRetry: 5, MaxRetryCredentials: 7}
	published := h.waitForPublished(want)
	runtime := issue6631ValuesOf(h.service.currentConfig())
	disk := h.disk()
	if !published || runtime != want || disk != want {
		t.Fatalf("after three acknowledged writes: published=%v runtime=%+v config.yaml=%+v, want %+v", published, runtime, disk, want)
	}
}

func TestManagementWriteDuringReloadReachesRuntime_Issue6631(t *testing.T) {
	h := newIssue6631Harness(t)

	h.put("debug", "true")
	h.awaitFirstReload()
	h.put("request-retry", "5")
	h.publishFirstReload()

	want := issue6631Values{Debug: true, RequestRetry: 5}
	published := h.waitForPublished(want)
	runtime := issue6631ValuesOf(h.service.currentConfig())
	disk := h.disk()
	if !published || runtime != want || disk != want {
		t.Fatalf("after two acknowledged writes: published=%v runtime=%+v config.yaml=%+v, want %+v", published, runtime, disk, want)
	}
}
