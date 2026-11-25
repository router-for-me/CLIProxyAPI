package pluginhost

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestHomeExecutorReadinessDuringReplacement(t *testing.T) {
	for _, outcome := range []string{"replacement", "rollback", "unsupported-quiesce"} {
		t.Run(outcome, func(t *testing.T) {
			quiescing, finishQuiesce := make(chan struct{}), make(chan struct{})
			registering, finishRegistration := make(chan struct{}), make(chan struct{})
			resumeQuiesce := sync.OnceFunc(func() { close(finishQuiesce) })
			resumeRegistration := sync.OnceFunc(func() { close(finishRegistration) })
			previous := newHomeLifecycleClient(func(method string) ([]byte, error) {
				switch method {
				case pluginabi.MethodPluginQuiesce:
					close(quiescing)
					<-finishQuiesce
					if outcome == "unsupported-quiesce" {
						return marshalRPCError("unknown_method", "quiesce not supported"), nil
					}
				case pluginabi.MethodPluginReconfigure:
					if outcome == "rollback" {
						close(registering)
						<-finishRegistration
					}
				}
				return nil, nil
			})
			replacement := newHomeLifecycleClient(func(method string) ([]byte, error) {
				if method == pluginabi.MethodPluginRegister {
					if outcome == "rollback" {
						return nil, errors.New("replacement registration failed")
					}
					close(registering)
					<-finishRegistration
				}
				return nil, nil
			})
			host, cfg := newHomeLifecycleHost(t, previous, replacement)
			old, errResolve := host.ResolveHomePluginExecutor(homePluginID, "antigravity")
			if errResolve != nil {
				t.Fatal(errResolve)
			}
			writeVersionedPluginFile(t, cfg.Plugins.Dir, homePluginID, "2.0.0")
			done := make(chan struct{})
			go func() {
				defer close(done)
				host.ApplyConfig(t.Context(), cfg)
			}()
			defer func() {
				resumeQuiesce()
				resumeRegistration()
				<-done
			}()

			checkReadiness := func(stage string, wantReady bool) coreauth.ProviderExecutor {
				t.Helper()
				ready := host.HomeExecutorPlugins()
				if gotReady := slices.Equal(ready[homePluginID], []string{"antigravity"}); gotReady != wantReady || !wantReady && len(ready) != 0 {
					t.Fatalf("%s: advertised plugins = %v, want ready = %v", stage, ready, wantReady)
				}
				adapter, err := host.ResolveHomePluginExecutor(homePluginID, "antigravity")
				if (err == nil) != wantReady {
					t.Fatalf("%s: resolution error = %v, want ready = %v", stage, err, wantReady)
				}
				return adapter
			}
			waitHomeLifecycle(t, quiescing, "quiesce RPC")
			checkReadiness("quiescing", false)
			auth := homeExecutorTestAuth("selected-token", "existing-project", time.Now().Add(time.Hour))
			req := coreexecutor.Request{Model: homePluginModel, Payload: []byte(`{}`)}
			_, errExecute := old.Execute(t.Context(), &auth, req, coreexecutor.Options{})
			assertHomeExecutorUnavailable(t, errExecute)

			resumeQuiesce()
			waitHomeLifecycle(t, registering, "registration RPC")
			checkReadiness("registering", outcome == "unsupported-quiesce")
			resumeRegistration()
			waitHomeLifecycle(t, done, "completed reload")
			current := checkReadiness("published", true)
			if _, errExecute := current.Execute(t.Context(), &auth, req, coreexecutor.Options{}); errExecute != nil {
				t.Fatalf("current executor: %v", errExecute)
			}
			_, errExecute = old.Execute(t.Context(), &auth, req, coreexecutor.Options{})
			if outcome == "rollback" {
				if errExecute != nil {
					t.Fatalf("recovered executor: %v", errExecute)
				}
			} else {
				assertHomeExecutorUnavailable(t, errExecute)
			}
		})
	}
}

func TestHomeExecutorDispatchLifecycleRejections(t *testing.T) {
	for _, operation := range []string{"execute", "stream", "count"} {
		for _, event := range []string{"reload-same-version", "rpc-quiesced", "client-closed"} {
			t.Run(operation+"/"+event, func(t *testing.T) {
				var oldCalls, newCalls atomic.Int32
				previous := newHomeLifecycleClient(func(method string) ([]byte, error) {
					switch method {
					case pluginabi.MethodExecutorExecute, pluginabi.MethodExecutorExecuteStream, pluginabi.MethodExecutorCountTokens:
						oldCalls.Add(1)
						return marshalRPCError("plugin_quiesced", "plugin stopped accepting requests", http.StatusServiceUnavailable), nil
					}
					return nil, nil
				})
				replacement := newHomeLifecycleClient(func(method string) ([]byte, error) {
					switch method {
					case pluginabi.MethodExecutorExecute, pluginabi.MethodExecutorExecuteStream, pluginabi.MethodExecutorCountTokens:
						newCalls.Add(1)
					}
					return nil, nil
				})
				host, cfg := newHomeLifecycleHost(t, previous, replacement)
				hook := &homeExecutorResultHook{}
				manager := coreauth.NewManager(nil, nil, hook)
				manager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
				manager.SetHomePluginExecutorResolver(host)
				manager.SetRetryConfig(2, 0, 2)
				auth := homeExecutorTestAuth("selected-token", "existing-project", time.Now().Add(time.Hour))
				dispatcher := &homeExecutorTestDispatcher{payload: homeExecutorDispatchJSON(t, auth, homePluginID)}
				registry := executionregistry.New()
				var releases int
				registry.SetReleaseSink(func(group executionregistry.ReleaseGroup, _ int64) {
					if group.CredentialID != homePluginAuth || group.Model != homePluginModel {
						t.Errorf("unexpected release group: %+v", group)
					}
					releases++
				})
				manager.PublishHomeDispatch(dispatcher, registry, 1)
				capture := newTestUsageCapturePlugin("")
				registerTestUsagePlugin(t, t.Name(), capture)
				req := coreexecutor.Request{Model: homePluginAlias, Payload: []byte(`{}`)}
				opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: operation == "stream"}
				intercepted := false
				opts.RequestAfterAuthInterceptor = func(ctx context.Context, _ coreexecutor.RequestAfterAuthInterceptRequest) coreexecutor.RequestAfterAuthInterceptResponse {
					intercepted = true
					switch event {
					case "reload-same-version":
						host.ApplyConfig(ctx, &config.Config{})
						host.ApplyConfig(ctx, cfg)
						if ready := host.HomeExecutorPlugins(); len(ready[homePluginID]) != 1 {
							t.Errorf("same-version replacement not ready: %v", ready)
						}
					case "client-closed":
						// Model a call losing the race with shutdown at the guarded RPC boundary.
						host.mu.Lock()
						client := host.loaded[homePluginID].client
						host.mu.Unlock()
						client.Shutdown()
					}
					return coreexecutor.RequestAfterAuthInterceptResponse{}
				}
				var errExecute error
				switch operation {
				case "execute":
					_, errExecute = manager.Execute(t.Context(), []string{"home"}, req, opts)
				case "stream":
					_, errExecute = manager.ExecuteStream(t.Context(), []string{"home"}, req, opts)
				case "count":
					_, errExecute = manager.ExecuteCount(t.Context(), []string{"home"}, req, opts)
				}
				assertHomeExecutorUnavailable(t, errExecute)
				var wantOldCalls int32
				if event == "rpc-quiesced" {
					wantOldCalls = 1
					if pluginErr, ok := errors.AsType[rpcError](errExecute); !ok || pluginErr.Code != "plugin_quiesced" {
						t.Errorf("quiesce RPC cause not preserved: %v", errExecute)
					}
				} else if event == "client-closed" && !errors.Is(errExecute, errPluginClientClosed) {
					t.Errorf("closed client cause not preserved: %v", errExecute)
				}
				if !intercepted || oldCalls.Load() != wantOldCalls || newCalls.Load() != 0 {
					t.Errorf("intercepted=%t, old calls=%d (want %d), new calls=%d (want 0)", intercepted, oldCalls.Load(), wantOldCalls, newCalls.Load())
				}
				if len(hook.results) != 0 || dispatcher.calls != 1 || releases != 1 || len(registry.FreezeInFlight(time.Now()).Executions) != 0 {
					t.Errorf("account results=%d, dispatches=%d, releases=%d", len(hook.results), dispatcher.calls, releases)
				}
				coreusage.PublishRecord(t.Context(), coreusage.Record{Provider: t.Name()})
				for {
					record := capture.waitRecord(t, 5*time.Second)
					if record.Provider == t.Name() {
						break
					}
					if record.AuthID == homePluginAuth {
						t.Errorf("unexpected account usage from lifecycle rejection: %+v", record)
					}
				}
			})
		}
	}
}

func newHomeLifecycleClient(intercept func(string) ([]byte, error)) *lifecycleTestClient {
	plugin := validTestPlugin(homePluginID)
	plugin.Capabilities = homeExecutorRecord(&fakeExecutor{}, "antigravity").plugin.Capabilities
	return &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
		if response, err := intercept(method); response != nil || err != nil {
			return response, err
		}
		switch method {
		case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
			return lifecycleRegistrationResult(plugin)
		case pluginabi.MethodExecutorIdentifier:
			return marshalRPCResult(rpcIdentifierResponse{Identifier: "home-lifecycle-test"})
		case pluginabi.MethodPluginQuiesce:
			return marshalRPCResult(rpcEmptyResponse{})
		case pluginabi.MethodExecutorExecute, pluginabi.MethodExecutorCountTokens:
			return marshalRPCResult(pluginapi.ExecutorResponse{Payload: []byte(`{}`)})
		default:
			return nil, fmt.Errorf("unexpected plugin method %s", method)
		}
	}}
}

func newHomeLifecycleHost(t *testing.T, clients ...pluginClient) (*Host, *config.Config) {
	t.Helper()
	host := NewForTest(&sequencePluginLoader{clients: clients})
	t.Cleanup(host.ShutdownAll)
	dir, _ := makeVersionedPluginDir(t, homePluginID, "1.0.0")
	cfg := &config.Config{Plugins: config.PluginsConfig{Enabled: true, Dir: dir, Configs: enabledPluginConfigs(homePluginID)}}
	host.ApplyConfig(t.Context(), cfg)
	return host, cfg
}

func waitHomeLifecycle(t *testing.T, signal <-chan struct{}, stage string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("did not reach %s", stage)
	}
}

func assertHomeExecutorUnavailable(t *testing.T, err error) {
	t.Helper()
	localErr, ok := errors.AsType[*coreauth.Error](err)
	if !ok || localErr.Code != coreauth.ErrorCodeHomeExecutorPluginUnavailable || localErr.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("error = %v, want local Home plugin unavailability (503)", err)
	}
}
