package pluginhost

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

func schedulerAndManagementTestPlugin(name string) pluginapi.Plugin {
	return pluginapi.Plugin{
		Metadata: pluginapi.Metadata{
			Name:             name,
			Version:          "1.0.0",
			Author:           "test",
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
		},
		Capabilities: pluginapi.Capabilities{
			Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
				return pluginapi.SchedulerPickResponse{}, nil
			}),
			ManagementAPI: &managementPluginDouble{},
		},
	}
}

func waitForHostTestValue[T any](t *testing.T, ch <-chan T, desc string) T {
	t.Helper()
	select {
	case val := <-ch:
		return val
	case <-time.After(5 * time.Second):
		var zero T
		t.Fatalf("timed out waiting for %s", desc)
		return zero
	}
}

func registerCloseOnceCleanup(t *testing.T, ch chan struct{}) func() {
	t.Helper()
	var once sync.Once
	release := func() {
		once.Do(func() { close(ch) })
	}
	t.Cleanup(release)
	return release
}

func TestHostApplyConfigRejectedReconfigurePreservesPreviousCapabilities(t *testing.T) {
	var logBuf bytes.Buffer
	prevOut := log.StandardLogger().Out
	prevLevel := log.GetLevel()
	log.SetOutput(&logBuf)
	log.SetLevel(log.WarnLevel)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetLevel(prevLevel)
	})

	client := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodPluginRegister:
			return lifecycleRegistrationResult(schedulerAndManagementTestPlugin("alpha"))
		case pluginabi.MethodPluginReconfigure:
			return marshalRPCError("reconfigure_rejected", "setting requires restart"), nil
		case pluginabi.MethodSchedulerPick:
			return marshalRPCResult(pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-1"})
		case pluginabi.MethodManagementRegister:
			return marshalRPCResult(rpcManagementRegistrationResponse{
				Routes: []pluginapi.ManagementRoute{{Method: http.MethodGet, Path: "/plugins/alpha/status"}},
			})
		case pluginabi.MethodManagementHandle:
			return marshalRPCResult(pluginapi.ManagementResponse{StatusCode: http.StatusOK, Body: []byte("ok")})
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}}

	h := NewForTest(&sequencePluginLoader{clients: []pluginClient{client}})
	t.Cleanup(h.ShutdownAll)
	pluginsDir, paths := makeVersionedPluginDir(t, "alpha", "1.0.0")

	cfg1 := versionedPluginHostConfig(t, pluginsDir, "1.0.0")
	h.ApplyConfig(context.Background(), cfg1)
	h.RegisterManagementRoutes(context.Background(), nil)

	resp, handled, errPick := h.PickAuth(context.Background(), schedulerRequest("auth-1", "auth-2"))
	if errPick != nil || !handled || resp.AuthID != "auth-1" {
		t.Fatalf("initial PickAuth() = (%#v, %v, %v), want auth-1", resp, handled, errPick)
	}

	logBuf.Reset()
	cfg2 := versionedPluginHostConfig(t, pluginsDir, "1.0.0")
	item := cfg2.Plugins.Configs["alpha"]
	item.Priority = 42
	cfg2.Plugins.Configs["alpha"] = item
	h.ApplyConfig(context.Background(), cfg2)
	h.RegisterManagementRoutes(context.Background(), nil)

	if !h.pluginIdentityCurrent("alpha", paths["1.0.0"], "1.0.0") {
		t.Fatal("plugin alpha was dropped from active snapshot after rejected reconfigure")
	}
	records := h.activeRecords()
	if len(records) != 1 || records[0].priority != 0 {
		t.Fatalf("active records after rejected reconfigure = %#v, want single record preserving priority 0", records)
	}
	resp, handled, errPick = h.PickAuth(context.Background(), schedulerRequest("auth-1", "auth-2"))
	if errPick != nil || !handled || resp.AuthID != "auth-1" {
		t.Fatalf("PickAuth() after rejected reconfigure = (%#v, %v, %v), want auth-1", resp, handled, errPick)
	}

	req := httptest.NewRequest(http.MethodGet, "/v0/management/plugins/alpha/status", nil)
	rec := httptest.NewRecorder()
	if !h.ServeManagementHTTP(rec, req) {
		t.Fatal("ServeManagementHTTP() = false after rejected reconfigure, want true")
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("ServeManagementHTTP() response = %d %q, want 200 ok", rec.Code, rec.Body.String())
	}
	if strings.Contains(logBuf.String(), "returned invalid metadata or no capabilities") {
		t.Fatalf("logs unexpectedly contained invalid metadata warning on rejected reconfigure: %s", logBuf.String())
	}
}

func TestHostApplyConfigReconfigurePostAcceptFailureRequiresRollback(t *testing.T) {
	pluginWithExecutorAndScheduler := func() pluginapi.Plugin {
		p := schedulerAndManagementTestPlugin("alpha")
		p.Capabilities.Executor = &fakeExecutor{}
		p.Capabilities.ExecutorAuthProviders = []string{"codex"}
		return p
	}

	t.Run("rollback_succeeds", func(t *testing.T) {
		var mu sync.Mutex
		var registeredYAML []byte
		var reconfigureYAMLs [][]byte
		var identifierCalls int

		client := &lifecycleTestClient{call: func(_ context.Context, method string, request []byte) ([]byte, error) {
			switch method {
			case pluginabi.MethodPluginRegister:
				var req rpcLifecycleRequest
				if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
					return nil, errUnmarshal
				}
				mu.Lock()
				registeredYAML = bytes.Clone(req.ConfigYAML)
				mu.Unlock()
				return lifecycleRegistrationResult(pluginWithExecutorAndScheduler())
			case pluginabi.MethodPluginReconfigure:
				var req rpcLifecycleRequest
				if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
					return nil, errUnmarshal
				}
				mu.Lock()
				reconfigureYAMLs = append(reconfigureYAMLs, bytes.Clone(req.ConfigYAML))
				mu.Unlock()
				return lifecycleRegistrationResult(pluginWithExecutorAndScheduler())
			case pluginabi.MethodExecutorIdentifier:
				mu.Lock()
				identifierCalls++
				callNum := identifierCalls
				mu.Unlock()
				if callNum == 2 {
					return marshalRPCError("identifier_failed", "post-accept identifier query failed"), nil
				}
				return marshalRPCResult(rpcIdentifierResponse{Identifier: "codex"})
			case pluginabi.MethodSchedulerPick:
				return marshalRPCResult(pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-1"})
			default:
				return nil, fmt.Errorf("unexpected method %s", method)
			}
		}}

		h := NewForTest(&sequencePluginLoader{clients: []pluginClient{client}})
		t.Cleanup(h.ShutdownAll)
		pluginsDir, paths := makeVersionedPluginDir(t, "alpha", "1.0.0")

		h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))

		cfg2 := versionedPluginHostConfig(t, pluginsDir, "1.0.0")
		item := cfg2.Plugins.Configs["alpha"]
		item.Priority = 15
		cfg2.Plugins.Configs["alpha"] = item
		h.ApplyConfig(context.Background(), cfg2)

		if !h.pluginIdentityCurrent("alpha", paths["1.0.0"], "1.0.0") {
			t.Fatal("plugin alpha was not restored after successful rollback reconfigure")
		}
		mu.Lock()
		gotReconfigures := len(reconfigureYAMLs)
		var lastYAML []byte
		if gotReconfigures > 0 {
			lastYAML = bytes.Clone(reconfigureYAMLs[gotReconfigures-1])
		}
		origYAML := bytes.Clone(registeredYAML)
		mu.Unlock()
		if gotReconfigures != 2 {
			t.Fatalf("reconfigure calls = %d, want 2 (failed attempt + rollback)", gotReconfigures)
		}
		if !bytes.Equal(lastYAML, origYAML) {
			t.Fatalf("rollback reconfigure config = %q, want original %q", lastYAML, origYAML)
		}
		records := h.activeRecords()
		if len(records) != 1 || records[0].priority != 0 {
			t.Fatalf("active records after rollback = %#v, want priority 0", records)
		}
		resp, handled, errPick := h.PickAuth(context.Background(), schedulerRequest("auth-1"))
		if errPick != nil || !handled || resp.AuthID != "auth-1" {
			t.Fatalf("PickAuth() after rollback = (%#v, %v, %v), want auth-1", resp, handled, errPick)
		}
	})

	t.Run("rollback_fails_drops_capabilities", func(t *testing.T) {
		var mu sync.Mutex
		var reconfigureCalls int
		var identifierCalls int

		client := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
			switch method {
			case pluginabi.MethodPluginRegister:
				return lifecycleRegistrationResult(pluginWithExecutorAndScheduler())
			case pluginabi.MethodPluginReconfigure:
				mu.Lock()
				reconfigureCalls++
				callNum := reconfigureCalls
				mu.Unlock()
				if callNum == 1 {
					return lifecycleRegistrationResult(pluginWithExecutorAndScheduler())
				}
				return marshalRPCError("rollback_failed", "unable to restore old config"), nil
			case pluginabi.MethodExecutorIdentifier:
				mu.Lock()
				identifierCalls++
				callNum := identifierCalls
				mu.Unlock()
				if callNum == 1 {
					return marshalRPCResult(rpcIdentifierResponse{Identifier: "codex"})
				}
				return marshalRPCError("identifier_failed", "post-accept identifier query failed"), nil
			default:
				return nil, fmt.Errorf("unexpected method %s", method)
			}
		}}

		h := NewForTest(&sequencePluginLoader{clients: []pluginClient{client}})
		t.Cleanup(h.ShutdownAll)
		pluginsDir, paths := makeVersionedPluginDir(t, "alpha", "1.0.0")

		h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))
		h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))

		if h.pluginIdentityCurrent("alpha", paths["1.0.0"], "1.0.0") {
			t.Fatal("plugin alpha remained active even though post-accept rollback reconfigure failed")
		}
		if h.HasScheduler() {
			t.Fatal("HasScheduler() = true after failed post-accept rollback, want false")
		}
		h.mu.Lock()
		needsRecovery := h.loaded["alpha"] != nil && h.loaded["alpha"].needsRecovery
		h.mu.Unlock()
		if !needsRecovery {
			t.Fatal("loaded plugin was not marked needsRecovery after failed post-accept rollback")
		}
	})

	t.Run("ok_envelope_invalid_result_rollback_succeeds", func(t *testing.T) {
		var mu sync.Mutex
		var registeredYAML []byte
		var reconfigureYAMLs [][]byte

		client := &lifecycleTestClient{call: func(_ context.Context, method string, request []byte) ([]byte, error) {
			switch method {
			case pluginabi.MethodPluginRegister:
				var req rpcLifecycleRequest
				if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
					return nil, errUnmarshal
				}
				mu.Lock()
				registeredYAML = bytes.Clone(req.ConfigYAML)
				mu.Unlock()
				return lifecycleRegistrationResult(schedulerAndManagementTestPlugin("alpha"))
			case pluginabi.MethodPluginReconfigure:
				var req rpcLifecycleRequest
				if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
					return nil, errUnmarshal
				}
				mu.Lock()
				reconfigureYAMLs = append(reconfigureYAMLs, bytes.Clone(req.ConfigYAML))
				callNum := len(reconfigureYAMLs)
				mu.Unlock()
				if callNum == 1 {
					return []byte(`{"ok":true,"result":"not-an-object"}`), nil
				}
				return lifecycleRegistrationResult(schedulerAndManagementTestPlugin("alpha"))
			case pluginabi.MethodSchedulerPick:
				return marshalRPCResult(pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-1"})
			default:
				return nil, fmt.Errorf("unexpected method %s", method)
			}
		}}

		h := NewForTest(&sequencePluginLoader{clients: []pluginClient{client}})
		t.Cleanup(h.ShutdownAll)
		pluginsDir, paths := makeVersionedPluginDir(t, "alpha", "1.0.0")

		h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))
		cfg2 := versionedPluginHostConfig(t, pluginsDir, "1.0.0")
		item := cfg2.Plugins.Configs["alpha"]
		item.Priority = 19
		cfg2.Plugins.Configs["alpha"] = item
		h.ApplyConfig(context.Background(), cfg2)

		if !h.pluginIdentityCurrent("alpha", paths["1.0.0"], "1.0.0") {
			t.Fatal("plugin alpha was not restored after ok-envelope decode failure rollback")
		}
		mu.Lock()
		gotCalls := len(reconfigureYAMLs)
		var lastYAML []byte
		if gotCalls > 0 {
			lastYAML = bytes.Clone(reconfigureYAMLs[gotCalls-1])
		}
		origYAML := bytes.Clone(registeredYAML)
		mu.Unlock()
		if gotCalls != 2 {
			t.Fatalf("reconfigure calls = %d, want 2 (ok-envelope decode failure + rollback)", gotCalls)
		}
		if !bytes.Equal(lastYAML, origYAML) {
			t.Fatalf("rollback reconfigure config = %q, want original %q", lastYAML, origYAML)
		}
	})

	t.Run("ok_envelope_invalid_result_rollback_fails_drops_capabilities", func(t *testing.T) {
		var reconfigureCalls atomic.Int32
		client := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
			switch method {
			case pluginabi.MethodPluginRegister:
				return lifecycleRegistrationResult(schedulerAndManagementTestPlugin("alpha"))
			case pluginabi.MethodPluginReconfigure:
				if reconfigureCalls.Add(1) == 1 {
					return []byte(`{"ok":true,"result":"not-an-object"}`), nil
				}
				return marshalRPCError("rollback_failed", "unable to restore old config"), nil
			default:
				return nil, fmt.Errorf("unexpected method %s", method)
			}
		}}

		h := NewForTest(&sequencePluginLoader{clients: []pluginClient{client}})
		t.Cleanup(h.ShutdownAll)
		pluginsDir, paths := makeVersionedPluginDir(t, "alpha", "1.0.0")

		h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))
		h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))

		if gotCalls := reconfigureCalls.Load(); gotCalls != 2 {
			t.Fatalf("reconfigure calls = %d, want 2 (ok-envelope decode failure + failed rollback)", gotCalls)
		}
		if h.pluginIdentityCurrent("alpha", paths["1.0.0"], "1.0.0") {
			t.Fatal("plugin alpha remained active after ok-envelope decode failure and failed rollback")
		}
		if h.HasScheduler() {
			t.Fatal("HasScheduler() = true after failed rollback, want false")
		}
		h.mu.Lock()
		needsRecovery := h.loaded["alpha"] != nil && h.loaded["alpha"].needsRecovery
		h.mu.Unlock()
		if !needsRecovery {
			t.Fatal("loaded plugin was not marked needsRecovery after failed rollback")
		}
	})
}

func TestHostPickAuthWaitsForHotReloadReplacement(t *testing.T) {
	quiesced := atomic.Bool{}
	var oldPicksAfterQuiesce atomic.Int32
	replacementRegisterStarted := make(chan struct{})
	releaseReplacementRegister := make(chan struct{})

	oldClient := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodPluginRegister:
			return lifecycleRegistrationResult(schedulerAndManagementTestPlugin("alpha"))
		case pluginabi.MethodPluginQuiesce:
			quiesced.Store(true)
			return marshalRPCResult(rpcEmptyResponse{})
		case pluginabi.MethodSchedulerPick:
			if quiesced.Load() {
				oldPicksAfterQuiesce.Add(1)
				return marshalRPCResult(pluginapi.SchedulerPickResponse{Handled: true, Reject: true, RejectCode: "quiesced", RejectReason: "plugin quiesced"})
			}
			return marshalRPCResult(pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-v1"})
		default:
			return nil, fmt.Errorf("unexpected old method %s", method)
		}
	}}
	replacementClient := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodPluginRegister:
			close(replacementRegisterStarted)
			<-releaseReplacementRegister
			return lifecycleRegistrationResult(schedulerAndManagementTestPlugin("alpha"))
		case pluginabi.MethodSchedulerPick:
			return marshalRPCResult(pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-v2"})
		default:
			return nil, fmt.Errorf("unexpected replacement method %s", method)
		}
	}}

	waitEntered := make(chan struct{})
	var waitOnce sync.Once
	h := NewForTest(&sequencePluginLoader{clients: []pluginClient{oldClient, replacementClient}})
	h.schedulerReloadWaitHook = func(pluginID string) {
		if pluginID == "alpha" {
			waitOnce.Do(func() { close(waitEntered) })
		}
	}
	t.Cleanup(h.ShutdownAll)
	releaseReplacement := registerCloseOnceCleanup(t, releaseReplacementRegister)
	pluginsDir, _ := makeVersionedPluginDir(t, "alpha", "1.0.0")

	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))
	writeVersionedPluginFile(t, pluginsDir, "alpha", "2.0.0")

	applyDone := make(chan struct{})
	go func() {
		h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "2.0.0"))
		close(applyDone)
	}()

	waitForHostTestSignal(t, replacementRegisterStarted, "replacement registration start")

	type pickResult struct {
		resp    pluginapi.SchedulerPickResponse
		handled bool
		err     error
	}
	pickDone := make(chan pickResult, 1)
	go func() {
		resp, handled, errPick := h.PickAuth(context.Background(), schedulerRequest("auth-v1", "auth-v2"))
		pickDone <- pickResult{resp: resp, handled: handled, err: errPick}
	}()

	waitForHostTestSignal(t, waitEntered, "PickAuth enters scheduler reload wait")
	releaseReplacement()
	waitForHostTestSignal(t, applyDone, "hot reload ApplyConfig completion")

	res := waitForHostTestValue(t, pickDone, "PickAuth result")
	if res.err != nil || !res.handled || res.resp.Reject || res.resp.AuthID != "auth-v2" {
		t.Fatalf("PickAuth() during hot reload = (%#v, %v, %v), want auth-v2 without rejection/error", res.resp, res.handled, res.err)
	}
	if got := oldPicksAfterQuiesce.Load(); got != 0 {
		t.Fatalf("old scheduler picks after quiesce = %d, want 0", got)
	}
}

func TestHostPickAuthRetriesMidCallRejectionDuringHotReload(t *testing.T) {
	t.Run("reload_still_in_progress", func(t *testing.T) {
		oldPickEntered := make(chan struct{})
		releaseOldPick := make(chan struct{})
		replacementRegisterStarted := make(chan struct{})
		releaseReplacementRegister := make(chan struct{})
		var releaseOldOnce sync.Once
		unblockOldPick := func() { releaseOldOnce.Do(func() { close(releaseOldPick) }) }

		oldClient := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
			switch method {
			case pluginabi.MethodPluginRegister:
				return lifecycleRegistrationResult(schedulerAndManagementTestPlugin("alpha"))
			case pluginabi.MethodPluginQuiesce:
				unblockOldPick()
				return marshalRPCResult(rpcEmptyResponse{})
			case pluginabi.MethodSchedulerPick:
				close(oldPickEntered)
				<-releaseOldPick
				return marshalRPCResult(pluginapi.SchedulerPickResponse{Handled: true, Reject: true, RejectCode: "quiescing", RejectReason: "released held pick"})
			default:
				return nil, fmt.Errorf("unexpected old method %s", method)
			}
		}}
		replacementClient := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
			switch method {
			case pluginabi.MethodPluginRegister:
				close(replacementRegisterStarted)
				<-releaseReplacementRegister
				return lifecycleRegistrationResult(schedulerAndManagementTestPlugin("alpha"))
			case pluginabi.MethodSchedulerPick:
				return marshalRPCResult(pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-v2"})
			default:
				return nil, fmt.Errorf("unexpected replacement method %s", method)
			}
		}}

		h := NewForTest(&sequencePluginLoader{clients: []pluginClient{oldClient, replacementClient}})
		t.Cleanup(h.ShutdownAll)
		t.Cleanup(unblockOldPick)
		releaseReplacement := registerCloseOnceCleanup(t, releaseReplacementRegister)
		pluginsDir, _ := makeVersionedPluginDir(t, "alpha", "1.0.0")
		h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))
		writeVersionedPluginFile(t, pluginsDir, "alpha", "2.0.0")

		type pickResult struct {
			resp    pluginapi.SchedulerPickResponse
			handled bool
			err     error
		}
		pickDone := make(chan pickResult, 1)
		go func() {
			resp, handled, errPick := h.PickAuth(context.Background(), schedulerRequest("auth-v1", "auth-v2"))
			pickDone <- pickResult{resp: resp, handled: handled, err: errPick}
		}()

		waitForHostTestSignal(t, oldPickEntered, "held pick enters old scheduler")

		applyDone := make(chan struct{})
		go func() {
			h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "2.0.0"))
			close(applyDone)
		}()

		waitForHostTestSignal(t, replacementRegisterStarted, "replacement registration start")
		releaseReplacement()
		waitForHostTestSignal(t, applyDone, "hot reload ApplyConfig completion")

		res := waitForHostTestValue(t, pickDone, "PickAuth mid-call reload result")
		if res.err != nil || !res.handled || res.resp.Reject || res.resp.AuthID != "auth-v2" {
			t.Fatalf("PickAuth() after mid-call reload rejection = (%#v, %v, %v), want auth-v2", res.resp, res.handled, res.err)
		}
	})

	t.Run("reload_already_finished", func(t *testing.T) {
		oldPickEntered := make(chan struct{})
		releaseOldPick := make(chan struct{})

		oldClient := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
			switch method {
			case pluginabi.MethodPluginRegister:
				return lifecycleRegistrationResult(schedulerAndManagementTestPlugin("alpha"))
			case pluginabi.MethodPluginQuiesce:
				return marshalRPCResult(rpcEmptyResponse{})
			case pluginabi.MethodSchedulerPick:
				close(oldPickEntered)
				<-releaseOldPick
				return marshalRPCResult(pluginapi.SchedulerPickResponse{Handled: true, Reject: true, RejectCode: "quiesced", RejectReason: "stale instance"})
			default:
				return nil, fmt.Errorf("unexpected old method %s", method)
			}
		}}
		replacementClient := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
			switch method {
			case pluginabi.MethodPluginRegister:
				return lifecycleRegistrationResult(schedulerAndManagementTestPlugin("alpha"))
			case pluginabi.MethodSchedulerPick:
				return marshalRPCResult(pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-v2"})
			default:
				return nil, fmt.Errorf("unexpected replacement method %s", method)
			}
		}}

		h := NewForTest(&sequencePluginLoader{clients: []pluginClient{oldClient, replacementClient}})
		t.Cleanup(h.ShutdownAll)
		unblockOldPick := registerCloseOnceCleanup(t, releaseOldPick)
		pluginsDir, _ := makeVersionedPluginDir(t, "alpha", "1.0.0")
		h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))
		writeVersionedPluginFile(t, pluginsDir, "alpha", "2.0.0")

		type pickResult struct {
			resp    pluginapi.SchedulerPickResponse
			handled bool
			err     error
		}
		pickDone := make(chan pickResult, 1)
		go func() {
			resp, handled, errPick := h.PickAuth(context.Background(), schedulerRequest("auth-v1", "auth-v2"))
			pickDone <- pickResult{resp: resp, handled: handled, err: errPick}
		}()

		waitForHostTestSignal(t, oldPickEntered, "held pick enters old scheduler")
		h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "2.0.0"))
		unblockOldPick()

		res := waitForHostTestValue(t, pickDone, "PickAuth completed reload result")
		if res.err != nil || !res.handled || res.resp.Reject || res.resp.AuthID != "auth-v2" {
			t.Fatalf("PickAuth() after completed reload rejection = (%#v, %v, %v), want auth-v2", res.resp, res.handled, res.err)
		}
	})

	t.Run("rollback_to_same_plugin", func(t *testing.T) {
		oldPickEntered := make(chan struct{})
		releaseOldPick := make(chan struct{})
		var releaseOldOnce sync.Once
		unblockOldPick := func() { releaseOldOnce.Do(func() { close(releaseOldPick) }) }
		var pickCount atomic.Int32
		var onceEnter sync.Once

		oldClient := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
			switch method {
			case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
				return lifecycleRegistrationResult(schedulerAndManagementTestPlugin("alpha"))
			case pluginabi.MethodPluginQuiesce:
				unblockOldPick()
				return marshalRPCResult(rpcEmptyResponse{})
			case pluginabi.MethodSchedulerPick:
				if pickCount.Add(1) == 1 {
					onceEnter.Do(func() { close(oldPickEntered) })
					<-releaseOldPick
					return marshalRPCResult(pluginapi.SchedulerPickResponse{Handled: true, Reject: true, RejectCode: "quiescing", RejectReason: "released held pick"})
				}
				return marshalRPCResult(pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-v1-recovered"})
			default:
				return nil, fmt.Errorf("unexpected old method %s", method)
			}
		}}
		failingReplacementClient := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
			if method == pluginabi.MethodPluginRegister {
				return nil, fmt.Errorf("replacement init failed")
			}
			return nil, fmt.Errorf("unexpected replacement method %s", method)
		}}

		h := NewForTest(&sequencePluginLoader{clients: []pluginClient{oldClient, failingReplacementClient}})
		t.Cleanup(h.ShutdownAll)
		t.Cleanup(unblockOldPick)
		pluginsDir, _ := makeVersionedPluginDir(t, "alpha", "1.0.0")
		h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))
		writeVersionedPluginFile(t, pluginsDir, "alpha", "2.0.0")

		type pickResult struct {
			resp    pluginapi.SchedulerPickResponse
			handled bool
			err     error
		}
		pickDone := make(chan pickResult, 1)
		go func() {
			resp, handled, errPick := h.PickAuth(context.Background(), schedulerRequest("auth-v1-recovered"))
			pickDone <- pickResult{resp: resp, handled: handled, err: errPick}
		}()

		waitForHostTestSignal(t, oldPickEntered, "held pick enters old scheduler")
		h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "2.0.0"))

		res := waitForHostTestValue(t, pickDone, "PickAuth rollback reload result")
		if res.err != nil || !res.handled || res.resp.Reject || res.resp.AuthID != "auth-v1-recovered" {
			t.Fatalf("PickAuth() after rollback reload = (%#v, %v, %v), want auth-v1-recovered", res.resp, res.handled, res.err)
		}
	})

	t.Run("unhandled_from_stale_instance", func(t *testing.T) {
		oldPickEntered := make(chan struct{})
		releaseOldPick := make(chan struct{})

		oldClient := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
			switch method {
			case pluginabi.MethodPluginRegister:
				return lifecycleRegistrationResult(schedulerAndManagementTestPlugin("alpha"))
			case pluginabi.MethodPluginQuiesce:
				return marshalRPCResult(rpcEmptyResponse{})
			case pluginabi.MethodSchedulerPick:
				close(oldPickEntered)
				<-releaseOldPick
				return marshalRPCResult(pluginapi.SchedulerPickResponse{Handled: false})
			default:
				return nil, fmt.Errorf("unexpected old method %s", method)
			}
		}}
		replacementClient := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
			switch method {
			case pluginabi.MethodPluginRegister:
				return lifecycleRegistrationResult(schedulerAndManagementTestPlugin("alpha"))
			case pluginabi.MethodSchedulerPick:
				return marshalRPCResult(pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-v2"})
			default:
				return nil, fmt.Errorf("unexpected replacement method %s", method)
			}
		}}

		h := NewForTest(&sequencePluginLoader{clients: []pluginClient{oldClient, replacementClient}})
		t.Cleanup(h.ShutdownAll)
		unblockOldPick := registerCloseOnceCleanup(t, releaseOldPick)
		pluginsDir, _ := makeVersionedPluginDir(t, "alpha", "1.0.0")
		h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))
		writeVersionedPluginFile(t, pluginsDir, "alpha", "2.0.0")

		type pickResult struct {
			resp    pluginapi.SchedulerPickResponse
			handled bool
			err     error
		}
		pickDone := make(chan pickResult, 1)
		go func() {
			resp, handled, errPick := h.PickAuth(context.Background(), schedulerRequest("auth-v1", "auth-v2"))
			pickDone <- pickResult{resp: resp, handled: handled, err: errPick}
		}()

		waitForHostTestSignal(t, oldPickEntered, "held pick enters old scheduler")
		h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "2.0.0"))
		unblockOldPick()

		res := waitForHostTestValue(t, pickDone, "PickAuth stale unhandled result")
		if res.err != nil || !res.handled || res.resp.Reject || res.resp.AuthID != "auth-v2" {
			t.Fatalf("PickAuth() after stale unhandled response = (%#v, %v, %v), want auth-v2", res.resp, res.handled, res.err)
		}
	})

	t.Run("wait_canceled_by_context", func(t *testing.T) {
		replacementRegisterStarted := make(chan struct{})
		releaseReplacementRegister := make(chan struct{})
		waitEntered := make(chan struct{})
		var waitOnce sync.Once

		oldClient := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
			switch method {
			case pluginabi.MethodPluginRegister:
				return lifecycleRegistrationResult(schedulerAndManagementTestPlugin("alpha"))
			case pluginabi.MethodPluginQuiesce:
				return marshalRPCResult(rpcEmptyResponse{})
			default:
				return nil, fmt.Errorf("unexpected old method %s", method)
			}
		}}
		replacementClient := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
			if method == pluginabi.MethodPluginRegister {
				close(replacementRegisterStarted)
				<-releaseReplacementRegister
				return lifecycleRegistrationResult(schedulerAndManagementTestPlugin("alpha"))
			}
			return nil, fmt.Errorf("unexpected replacement method %s", method)
		}}

		h := NewForTest(&sequencePluginLoader{clients: []pluginClient{oldClient, replacementClient}})
		h.schedulerReloadWaitHook = func(pluginID string) {
			if pluginID == "alpha" {
				waitOnce.Do(func() { close(waitEntered) })
			}
		}
		t.Cleanup(h.ShutdownAll)
		releaseReplacement := registerCloseOnceCleanup(t, releaseReplacementRegister)
		pluginsDir, _ := makeVersionedPluginDir(t, "alpha", "1.0.0")
		h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))
		writeVersionedPluginFile(t, pluginsDir, "alpha", "2.0.0")

		applyDone := make(chan struct{})
		go func() {
			h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "2.0.0"))
			close(applyDone)
		}()
		waitForHostTestSignal(t, replacementRegisterStarted, "replacement registration start")

		pickCtx, cancelPick := context.WithCancel(context.Background())
		t.Cleanup(cancelPick)
		type pickResult struct {
			handled bool
			err     error
		}
		pickDone := make(chan pickResult, 1)
		go func() {
			_, handled, errPick := h.PickAuth(pickCtx, schedulerRequest("auth-v1"))
			pickDone <- pickResult{handled: handled, err: errPick}
		}()

		waitForHostTestSignal(t, waitEntered, "PickAuth enters scheduler reload wait")
		cancelPick()

		res := waitForHostTestValue(t, pickDone, "PickAuth canceled reload wait result")
		if !res.handled || res.err != context.Canceled {
			t.Fatalf("PickAuth() after ctx cancel during reload wait = (%v, %v), want (true, context.Canceled)", res.handled, res.err)
		}
		releaseReplacement()
		waitForHostTestSignal(t, applyDone, "hot reload ApplyConfig completion")
	})
}

func TestHostSchedulerDiscoveryConsistentDuringHotReloadPublish(t *testing.T) {
	const swapCount = 24
	clients := make([]pluginClient, 0, swapCount+1)
	for i := 0; i <= swapCount; i++ {
		authID := fmt.Sprintf("auth-1")
		clients = append(clients, &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
			switch method {
			case pluginabi.MethodPluginRegister:
				return lifecycleRegistrationResult(schedulerAndManagementTestPlugin("alpha"))
			case pluginabi.MethodPluginQuiesce:
				return marshalRPCResult(rpcEmptyResponse{})
			case pluginabi.MethodSchedulerPick:
				return marshalRPCResult(pluginapi.SchedulerPickResponse{Handled: true, AuthID: authID})
			default:
				return nil, fmt.Errorf("unexpected method %s", method)
			}
		}})
	}

	loader := &sequencePluginLoader{clients: clients}
	h := NewForTest(loader)
	t.Cleanup(h.ShutdownAll)
	pluginsDir, _ := makeVersionedPluginDir(t, "alpha", "1.0.0")

	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))
	writeVersionedPluginFile(t, pluginsDir, "alpha", "2.0.0")

	stopReaders := make(chan struct{})
	errCh := make(chan error, 8)
	var wg sync.WaitGroup
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopReaders:
					return
				default:
				}
				if !h.HasScheduler() {
					select {
					case errCh <- fmt.Errorf("HasScheduler() returned false during hot reload publish"):
					default:
					}
					return
				}
				resp, handled, errPick := h.PickAuth(context.Background(), schedulerRequest("auth-1"))
				if errPick != nil || !handled || resp.Reject || resp.AuthID != "auth-1" {
					select {
					case errCh <- fmt.Errorf("PickAuth() during publish = (%#v, %v, %v), want handled auth-1", resp, handled, errPick):
					default:
					}
					return
				}
			}
		}()
	}

	for i := 1; i <= swapCount; i++ {
		version := "1.0.0"
		if i%2 == 1 {
			version = "2.0.0"
		}
		h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, version))
	}
	close(stopReaders)
	wg.Wait()

	select {
	case errReader := <-errCh:
		t.Fatal(errReader)
	default:
	}
	if loader.calls != swapCount+1 {
		t.Fatalf("loader.calls = %d, want %d real hot-reload opens", loader.calls, swapCount+1)
	}
}

func TestManagerExecuteHotReloadFromSingleTierToAcrossPriorities(t *testing.T) {
	oldPickEntered := make(chan struct{})
	releaseOldPick := make(chan struct{})
	var releaseOldOnce sync.Once
	unblockOldPick := func() { releaseOldOnce.Do(func() { close(releaseOldPick) }) }

	var mu sync.Mutex
	var v1Seen []string
	var v2Seen []string

	singleTierPlugin := schedulerAndManagementTestPlugin("alpha")
	singleTierPlugin.Capabilities.SchedulerAcrossPriorities = false
	acrossTierPlugin := schedulerAndManagementTestPlugin("alpha")
	acrossTierPlugin.Capabilities.SchedulerAcrossPriorities = true

	oldClient := &lifecycleTestClient{call: func(_ context.Context, method string, request []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodPluginRegister:
			return lifecycleRegistrationResult(singleTierPlugin)
		case pluginabi.MethodPluginQuiesce:
			unblockOldPick()
			return marshalRPCResult(rpcEmptyResponse{})
		case pluginabi.MethodSchedulerPick:
			var req pluginapi.SchedulerPickRequest
			if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
				return nil, errUnmarshal
			}
			mu.Lock()
			for _, candidate := range req.Candidates {
				v1Seen = append(v1Seen, candidate.ID)
			}
			mu.Unlock()
			close(oldPickEntered)
			<-releaseOldPick
			return marshalRPCResult(pluginapi.SchedulerPickResponse{Handled: true, Reject: true, RejectCode: "quiescing", RejectReason: "released held pick"})
		default:
			return nil, fmt.Errorf("unexpected old method %s", method)
		}
	}}
	replacementClient := &lifecycleTestClient{call: func(_ context.Context, method string, request []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodPluginRegister:
			return lifecycleRegistrationResult(acrossTierPlugin)
		case pluginabi.MethodSchedulerPick:
			var req pluginapi.SchedulerPickRequest
			if errUnmarshal := json.Unmarshal(request, &req); errUnmarshal != nil {
				return nil, errUnmarshal
			}
			mu.Lock()
			for _, candidate := range req.Candidates {
				v2Seen = append(v2Seen, candidate.ID)
			}
			mu.Unlock()
			return marshalRPCResult(pluginapi.SchedulerPickResponse{Handled: true, AuthID: "codex-low"})
		default:
			return nil, fmt.Errorf("unexpected replacement method %s", method)
		}
	}}

	h := NewForTest(&sequencePluginLoader{clients: []pluginClient{oldClient, replacementClient}})
	t.Cleanup(h.ShutdownAll)
	t.Cleanup(unblockOldPick)
	pluginsDir, _ := makeVersionedPluginDir(t, "alpha", "1.0.0")
	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))
	writeVersionedPluginFile(t, pluginsDir, "alpha", "2.0.0")

	manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
	var executedAuthID string
	manager.RegisterExecutor(stubSchedulerExecutor{id: "codex", lastAuthID: &executedAuthID})
	manager.SetPluginScheduler(h)
	for _, candidate := range []*coreauth.Auth{
		{ID: "codex-high", Provider: "codex", Attributes: map[string]string{"priority": "100"}},
		{ID: "codex-low", Provider: "codex", Attributes: map[string]string{"priority": "10"}},
	} {
		if _, errReg := manager.Register(context.Background(), candidate); errReg != nil {
			t.Fatalf("Register(%s) error = %v", candidate.ID, errReg)
		}
	}

	execErrCh := make(chan error, 1)
	go func() {
		_, errExec := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{}, cliproxyexecutor.Options{})
		execErrCh <- errExec
	}()

	waitForHostTestSignal(t, oldPickEntered, "Manager.Execute pick enters old single-tier scheduler")
	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "2.0.0"))

	if errExec := waitForHostTestValue(t, execErrCh, "Manager.Execute completion"); errExec != nil {
		t.Fatalf("Manager.Execute() error = %v, want nil", errExec)
	}
	mu.Lock()
	gotV1 := slices.Clone(v1Seen)
	gotV2 := slices.Clone(v2Seen)
	mu.Unlock()
	if !slices.Equal(gotV1, []string{"codex-high"}) {
		t.Fatalf("v1 single-tier scheduler saw %v, want [codex-high]", gotV1)
	}
	if !slices.Contains(gotV2, "codex-high") || !slices.Contains(gotV2, "codex-low") {
		t.Fatalf("v2 across-priorities replacement saw %v, want both codex-high and codex-low", gotV2)
	}
	if executedAuthID != "codex-low" {
		t.Fatalf("executedAuthID = %q, want codex-low", executedAuthID)
	}
}

func TestHostApplyConfigPluginDirReadErrorPreservesSnapshot(t *testing.T) {
	client := &lifecycleTestClient{call: func(_ context.Context, method string, _ []byte) ([]byte, error) {
		switch method {
		case pluginabi.MethodPluginRegister:
			return lifecycleRegistrationResult(schedulerAndManagementTestPlugin("alpha"))
		case pluginabi.MethodSchedulerPick:
			return marshalRPCResult(pluginapi.SchedulerPickResponse{Handled: true, AuthID: "auth-1"})
		case pluginabi.MethodManagementRegister:
			return marshalRPCResult(rpcManagementRegistrationResponse{
				Routes: []pluginapi.ManagementRoute{{Method: http.MethodGet, Path: "/plugins/alpha/status"}},
			})
		case pluginabi.MethodManagementHandle:
			return marshalRPCResult(pluginapi.ManagementResponse{StatusCode: http.StatusOK, Body: []byte("ok")})
		default:
			return nil, fmt.Errorf("unexpected method %s", method)
		}
	}}

	h := NewForTest(&sequencePluginLoader{clients: []pluginClient{client}})
	t.Cleanup(h.ShutdownAll)
	pluginsDir, paths := makeVersionedPluginDir(t, "alpha", "1.0.0")

	h.ApplyConfig(context.Background(), versionedPluginHostConfig(t, pluginsDir, "1.0.0"))
	h.RegisterManagementRoutes(context.Background(), nil)

	notADirPath := filepath.Join(t.TempDir(), "not-a-dir")
	if errWrite := os.WriteFile(notADirPath, []byte("file"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	badCfg := versionedPluginHostConfig(t, notADirPath, "1.0.0")
	h.ApplyConfig(context.Background(), badCfg)

	if !h.pluginIdentityCurrent("alpha", paths["1.0.0"], "1.0.0") {
		t.Fatal("plugin alpha was cleared from active snapshot on plugin dir read error")
	}
	resp, handled, errPick := h.PickAuth(context.Background(), schedulerRequest("auth-1", "auth-2"))
	if errPick != nil || !handled || resp.AuthID != "auth-1" {
		t.Fatalf("PickAuth() after plugin dir read error = (%#v, %v, %v), want auth-1", resp, handled, errPick)
	}
	req := httptest.NewRequest(http.MethodGet, "/v0/management/plugins/alpha/status", nil)
	rec := httptest.NewRecorder()
	if !h.ServeManagementHTTP(rec, req) {
		t.Fatal("ServeManagementHTTP() = false after plugin dir read error, want true")
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "ok" {
		t.Fatalf("ServeManagementHTTP() response = %d %q, want 200 ok", rec.Code, rec.Body.String())
	}
}
