package cliproxy

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

type serviceTestPluginExecutor struct{}

func (serviceTestPluginExecutor) Identifier() string {
	return "plugin-provider"
}

func (serviceTestPluginExecutor) Execute(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (serviceTestPluginExecutor) ExecuteStream(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (serviceTestPluginExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (serviceTestPluginExecutor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (serviceTestPluginExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestRegisterAvailableExecutors(t *testing.T) {
	oldRegisterPluginExecutors := registerPluginExecutors
	pluginRegisterCalls := 0
	var expectedPluginHost *pluginhost.Host
	var expectedManager *coreauth.Manager
	registerPluginExecutors = func(host *pluginhost.Host, manager *coreauth.Manager) {
		pluginRegisterCalls++
		if host != expectedPluginHost {
			t.Fatalf("plugin executor registration host = %p, want %p", host, expectedPluginHost)
		}
		if manager != expectedManager {
			t.Fatalf("plugin executor registration manager = %p, want %p", manager, expectedManager)
		}
		manager.RegisterExecutor(serviceTestPluginExecutor{})
	}
	t.Cleanup(func() {
		registerPluginExecutors = oldRegisterPluginExecutors
	})

	service := &Service{
		cfg:         &config.Config{},
		coreManager: coreauth.NewManager(nil, nil, nil),
		pluginHost:  pluginhost.New(),
	}
	expectedPluginHost = service.pluginHost
	expectedManager = service.coreManager
	service.ensureWebsocketGateway()

	service.registerAvailableExecutors(nil, executorRegistrationOptions{
		includeBaseline: true,
		includePlugins:  true,
	})

	if pluginRegisterCalls != 1 {
		t.Fatalf("plugin executor registration calls = %d, want 1", pluginRegisterCalls)
	}

	providers := []string{
		"codex",
		"claude",
		"gemini",
		"vertex",
		"aistudio",
		"antigravity",
		"kimi",
		"xai",
		"openai-compatibility",
		"plugin-provider",
	}
	for _, provider := range providers {
		resolved, ok := service.coreManager.Executor(provider)
		if !ok || resolved == nil {
			t.Fatalf("expected executor for provider %s after registration", provider)
		}
	}

	resolved, _ := service.coreManager.Executor("plugin-provider")
	if _, isPlugin := resolved.(serviceTestPluginExecutor); !isPlugin {
		t.Fatalf("executor type = %T, want serviceTestPluginExecutor", resolved)
	}
}
