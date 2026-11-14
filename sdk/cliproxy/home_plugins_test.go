package cliproxy

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/homeplugins"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
	sdkpluginstore "github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginstore"
	"gopkg.in/yaml.v3"
)

func TestSyncHomePluginsSkipsUnchangedSignature(t *testing.T) {
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	cfg.Plugins.Enabled = true
	cfg.Plugins.Configs = map[string]config.PluginInstanceConfig{}

	service := &Service{homePluginSyncFetch: func(context.Context, sdkpluginstore.PluginSyncRequest) (sdkpluginstore.PluginSyncResponse, error) {
		return sdkpluginstore.PluginSyncResponse{
			SchemaVersion: sdkpluginstore.PluginSyncSchemaVersion,
			ExpiresAt:     time.Now().UTC().Add(time.Minute),
			Items:         []sdkpluginstore.PluginSyncItem{},
		}, nil
	}}
	report, key, didSync, errSync := service.syncHomePlugins(context.Background(), cfg)
	if errSync != nil {
		t.Fatalf("syncHomePlugins() error = %v", errSync)
	}
	if !didSync || key == "" || !report.OK {
		t.Fatalf("syncHomePlugins() didSync=%v key=%q report=%+v, want reportable empty plan", didSync, key, report)
	}
	service.markHomePluginsSynced(key)

	_, gotKey, didSync, errSync := service.syncHomePlugins(context.Background(), cfg)
	if errSync != nil {
		t.Fatalf("syncHomePlugins(second) error = %v", errSync)
	}
	if didSync || gotKey != key {
		t.Fatalf("syncHomePlugins(second) didSync=%v key=%q, want skipped same key %q", didSync, gotKey, key)
	}
}

func TestSyncHomePluginsFetchFailureReturnsFailureReport(t *testing.T) {
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	cfg.Plugins.Enabled = true
	cfg.Plugins.Configs = map[string]config.PluginInstanceConfig{}
	wantErr := errors.New("plugin sync unavailable")
	service := &Service{homePluginSyncFetch: func(context.Context, sdkpluginstore.PluginSyncRequest) (sdkpluginstore.PluginSyncResponse, error) {
		return sdkpluginstore.PluginSyncResponse{}, wantErr
	}}

	report, key, didSync, errSync := service.syncHomePlugins(context.Background(), cfg)
	if !errors.Is(errSync, wantErr) {
		t.Fatalf("syncHomePlugins() error = %v, want %v", errSync, wantErr)
	}
	if didSync {
		t.Fatalf("syncHomePlugins() didSync = true, want false before a plan is available")
	}
	if key == "" {
		t.Fatal("syncHomePlugins() key is empty")
	}
	if report.SchemaVersion != 1 || report.Task != "plugin-sync" || report.OK || report.Error != wantErr.Error() {
		t.Fatalf("syncHomePlugins() report = %#v, want reportable fetch failure", report)
	}
}

func TestSyncHomePluginsFallsBackForUnsupportedHomeProtocol(t *testing.T) {
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	cfg.Plugins.Enabled = true
	cfg.Plugins.Dir = t.TempDir()
	cfg.Plugins.Configs = map[string]config.PluginInstanceConfig{}
	service := &Service{homePluginSyncFetch: func(context.Context, sdkpluginstore.PluginSyncRequest) (sdkpluginstore.PluginSyncResponse, error) {
		return sdkpluginstore.PluginSyncResponse{}, home.ErrPluginSyncUnsupported
	}}

	report, key, didSync, errSync := service.syncHomePlugins(context.Background(), cfg)
	if errSync != nil {
		t.Fatalf("syncHomePlugins() error = %v", errSync)
	}
	if !didSync || key == "" {
		t.Fatalf("syncHomePlugins() didSync=%v key=%q, want legacy fallback", didSync, key)
	}
	if !report.OK || report.Task != "plugin-sync" {
		t.Fatalf("syncHomePlugins() report = %#v, want successful legacy sync", report)
	}
}

func TestSyncHomePluginsSkipsFetchWhenPluginsDisabled(t *testing.T) {
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	cfg.Plugins.Configs = map[string]config.PluginInstanceConfig{}
	fetchCalls := 0
	service := &Service{homePluginSyncFetch: func(context.Context, sdkpluginstore.PluginSyncRequest) (sdkpluginstore.PluginSyncResponse, error) {
		fetchCalls++
		return sdkpluginstore.PluginSyncResponse{}, errors.New("fetch should not be called")
	}}

	report, key, didSync, errSync := service.syncHomePlugins(context.Background(), cfg)
	if errSync != nil {
		t.Fatalf("syncHomePlugins() error = %v", errSync)
	}
	if didSync || fetchCalls != 0 {
		t.Fatalf("syncHomePlugins() didSync=%v fetchCalls=%d, want disabled skip", didSync, fetchCalls)
	}
	if key == "" || report.Task != "plugin-sync" || !report.OK {
		t.Fatalf("disabled sync key/report = %q/%#v, want reportable disabled status", key, report)
	}
	if service.homePluginSyncKey != "" {
		t.Fatalf("homePluginSyncKey = %q, want caller to mark after reporting", service.homePluginSyncKey)
	}
}

func TestSyncHomePluginsSkipsDisabledReportWhenUnchanged(t *testing.T) {
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	cfg.Plugins.Configs = map[string]config.PluginInstanceConfig{}
	service := &Service{homePluginSyncFetch: func(context.Context, sdkpluginstore.PluginSyncRequest) (sdkpluginstore.PluginSyncResponse, error) {
		return sdkpluginstore.PluginSyncResponse{}, errors.New("fetch should not be called")
	}}

	report, key, didSync, errSync := service.syncHomePlugins(context.Background(), cfg)
	if errSync != nil {
		t.Fatalf("syncHomePlugins() error = %v", errSync)
	}
	if didSync || key == "" || report.Task != "plugin-sync" || !report.OK {
		t.Fatalf("syncHomePlugins() didSync=%v key=%q report=%#v, want reportable disabled status", didSync, key, report)
	}
	service.markHomePluginsSynced(key)

	report, gotKey, didSync, errSync := service.syncHomePlugins(context.Background(), cfg)
	if errSync != nil {
		t.Fatalf("syncHomePlugins(second) error = %v", errSync)
	}
	if didSync || gotKey != key || report.Task != "" {
		t.Fatalf("syncHomePlugins(second) didSync=%v key=%q report=%#v, want skipped unchanged disabled status", didSync, gotKey, report)
	}
}

func TestApplyHomeOverlayReturnsRuntimePluginSyncFailureWithoutApplyingConfig(t *testing.T) {
	base := &config.Config{}
	base.Home.Enabled = true
	base.Plugins.Enabled = true
	service := &Service{cfg: base}

	enabled := true
	remote := &config.Config{}
	remote.Plugins.Enabled = true
	remote.Plugins.Configs = map[string]config.PluginInstanceConfig{
		"broken": {
			Enabled: &enabled,
			Raw: yaml.Node{
				Kind: yaml.MappingNode,
				Tag:  "!!map",
				Content: []*yaml.Node{
					{Kind: yaml.ScalarNode, Tag: "!!str", Value: "store"},
					{
						Kind: yaml.MappingNode,
						Tag:  "!!map",
						Content: []*yaml.Node{
							{Kind: yaml.ScalarNode, Tag: "!!str", Value: "id"},
							{Kind: yaml.ScalarNode, Tag: "!!str", Value: "broken"},
						},
					},
				},
			},
		},
	}

	if errApply := service.applyHomeOverlayContext(context.Background(), remote); errApply == nil {
		t.Fatal("applyHomeOverlayContext() error = nil, want plugin sync failure")
	}
	if service.cfg == nil || !service.cfg.Home.Enabled || len(service.cfg.Plugins.Configs) != 0 {
		t.Fatalf("service cfg = %+v, want unchanged config after plugin sync failure", service.cfg)
	}
	if service.homePluginSyncKey != "" {
		t.Fatalf("homePluginSyncKey = %q, want empty after plugin sync failure", service.homePluginSyncKey)
	}
}

func TestStartHomeSubscriberDoesNotPreMarkPluginSync(t *testing.T) {
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	cfg.Home.Host = "127.0.0.1"
	cfg.Home.Port = 1
	cfg.Plugins.Enabled = true
	cfg.Plugins.Configs = map[string]config.PluginInstanceConfig{}
	service := &Service{cfg: cfg}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	service.startHomeSubscriber(ctx)
	defer func() {
		home.ClearCurrent()
		if service.homeCancel != nil {
			service.homeCancel()
		}
		if service.homeClient != nil {
			service.homeClient.Close()
		}
	}()

	if service.homePluginSyncKey != "" {
		t.Fatalf("homePluginSyncKey = %q, want empty before a successful plugin sync", service.homePluginSyncKey)
	}
}

func TestFinalizeHomePluginWorkRetriesFailedStatusWithoutMarkingSynced(t *testing.T) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	var writes atomic.Int32
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				reader := bufio.NewReader(conn)
				for {
					args, errRead := readRegistryTestRedisCommand(reader)
					if errRead != nil {
						return
					}
					switch {
					case len(args) > 0 && strings.EqualFold(args[0], "HELLO"):
						if _, errWrite := io.WriteString(conn, "%6\r\n$6\r\nserver\r\n$5\r\nredis\r\n$5\r\nproto\r\n:3\r\n$2\r\nid\r\n:1\r\n$4\r\nmode\r\n$10\r\nstandalone\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$7\r\nmodules\r\n*0\r\n"); errWrite != nil {
							return
						}
					case len(args) >= 2 && strings.EqualFold(args[0], "RPUSH") && args[1] == "plugin-status":
						if writes.Add(1) == 1 {
							if _, errWrite := io.WriteString(conn, "-ERR blocked\r\n"); errWrite != nil {
								return
							}
							continue
						}
						if _, errWrite := io.WriteString(conn, ":1\r\n"); errWrite != nil {
							return
						}
					default:
						if _, errWrite := io.WriteString(conn, "+OK\r\n"); errWrite != nil {
							return
						}
					}
				}
			}(conn)
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-serverDone
	})

	host, portText, errSplit := net.SplitHostPort(listener.Addr().String())
	if errSplit != nil {
		t.Fatalf("split listener address: %v", errSplit)
	}
	port, errPort := strconv.Atoi(portText)
	if errPort != nil {
		t.Fatalf("parse port: %v", errPort)
	}
	client := home.New(config.HomeConfig{Enabled: true, Host: host, Port: port})
	t.Cleanup(client.Close)
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	cfg.Home.NodeID = "node-1"
	service := &Service{}
	work := &homePluginFinalization{
		statusWork: []homePluginStatusWork{{cfg: cfg, report: homeplugins.CompletedSyncReport(homeplugins.CurrentPlatform(), nil)}},
		syncKey:    "sync-key",
		markSynced: true,
	}
	if errFinalize := service.finalizeHomePluginWork(context.Background(), client, work); errFinalize == nil {
		t.Fatal("first plugin status finalization succeeded, want Home rejection")
	}
	if service.homePluginSyncKey != "" || work.nextStatus != 0 || !work.markSynced {
		t.Fatalf("failed finalization marked or advanced work: key=%q next=%d marked=%v", service.homePluginSyncKey, work.nextStatus, work.markSynced)
	}
	if errFinalize := service.finalizeHomePluginWork(context.Background(), client, work); errFinalize != nil {
		t.Fatalf("retry finalization error = %v", errFinalize)
	}
	if service.homePluginSyncKey != "sync-key" || work.nextStatus != 1 || work.markSynced {
		t.Fatalf("successful finalization state: key=%q next=%d marked=%v", service.homePluginSyncKey, work.nextStatus, work.markSynced)
	}
	if errFinalize := service.finalizeHomePluginWork(context.Background(), client, work); errFinalize != nil {
		t.Fatalf("duplicate finalization error = %v", errFinalize)
	}
	if got := writes.Load(); got != 2 {
		t.Fatalf("plugin status writes = %d, want one failed write and one successful retry", got)
	}
}

func TestStageHomePluginTasksDefersDeleteUntilFinalization(t *testing.T) {
	client, _ := newHomePluginTaskTestClient(t, []home.PluginTask{{ID: 7, Operation: "delete", PluginID: "plugin-a"}}, 0)
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	cfg.Home.NodeID = "node-1"
	var deletes atomic.Int32
	service := &Service{homePluginDeleteTask: func(_ context.Context, _ *config.Config, task home.PluginTask) homeplugins.SyncReport {
		deletes.Add(1)
		return homeplugins.DeleteWithReport(context.Background(), nil, nil, task.ID, task.PluginID)
	}}

	taskWork, errStage := service.stageHomePluginTasksWithClient(context.Background(), cfg, client)
	if errStage != nil {
		t.Fatalf("stageHomePluginTasksWithClient() error = %v", errStage)
	}
	if got := deletes.Load(); got != 0 {
		t.Fatalf("staged plugin deletes = %d, want 0 before controlled finalization", got)
	}
	if len(taskWork) != 1 || taskWork[0].task.ID != 7 {
		t.Fatalf("staged task work = %#v, want delete task 7", taskWork)
	}

	if errFinalize := service.finalizeHomePluginWork(context.Background(), client, &homePluginFinalization{taskWork: taskWork}); errFinalize != nil {
		t.Fatalf("finalizeHomePluginWork() error = %v", errFinalize)
	}
	if got := deletes.Load(); got != 1 {
		t.Fatalf("finalized plugin deletes = %d, want 1", got)
	}
}

func TestFinalizeHomePluginTaskStatusRetryDoesNotRepeatDelete(t *testing.T) {
	client, writes := newHomePluginTaskTestClient(t, nil, 1)
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	cfg.Home.NodeID = "node-1"
	var deletes atomic.Int32
	service := &Service{homePluginDeleteTask: func(_ context.Context, _ *config.Config, task home.PluginTask) homeplugins.SyncReport {
		deletes.Add(1)
		return homeplugins.DeleteWithReport(context.Background(), nil, nil, task.ID, task.PluginID)
	}}
	work := &homePluginFinalization{taskWork: []homePluginTaskWork{{cfg: cfg, task: home.PluginTask{ID: 8, Operation: "delete", PluginID: "plugin-b"}}}}

	if errFinalize := service.finalizeHomePluginWork(context.Background(), client, work); errFinalize == nil {
		t.Fatal("first task report finalization succeeded, want Home rejection")
	}
	if got := deletes.Load(); got != 1 {
		t.Fatalf("first finalization deletes = %d, want 1", got)
	}
	if work.nextTask != 0 || work.taskWork[0].report == nil {
		t.Fatalf("failed task status did not retain action result: next=%d report=%#v", work.nextTask, work.taskWork[0].report)
	}
	if errFinalize := service.finalizeHomePluginWork(context.Background(), client, work); errFinalize != nil {
		t.Fatalf("retry finalization error = %v", errFinalize)
	}
	if got := deletes.Load(); got != 1 {
		t.Fatalf("retried finalization deletes = %d, want 1", got)
	}
	if work.nextTask != 1 {
		t.Fatalf("task finalization next = %d, want 1", work.nextTask)
	}
	if gotWrites := writes.Load(); gotWrites != 2 {
		t.Fatalf("task status writes = %d, want 2", gotWrites)
	}
}

func newHomePluginTaskTestClient(t *testing.T, tasks []home.PluginTask, failStatuses int32) (*home.Client, *atomic.Int32) {
	t.Helper()
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	rawTasks, errMarshal := json.Marshal(tasks)
	if errMarshal != nil {
		t.Fatalf("marshal tasks: %v", errMarshal)
	}
	var writes atomic.Int32
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				reader := bufio.NewReader(conn)
				for {
					args, errRead := readRegistryTestRedisCommand(reader)
					if errRead != nil {
						return
					}
					switch {
					case len(args) > 0 && strings.EqualFold(args[0], "HELLO"):
						_, _ = io.WriteString(conn, "%6\r\n$6\r\nserver\r\n$5\r\nredis\r\n$5\r\nproto\r\n:3\r\n$2\r\nid\r\n:1\r\n$4\r\nmode\r\n$10\r\nstandalone\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$7\r\nmodules\r\n*0\r\n")
					case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == "plugin-tasks":
						_, _ = io.WriteString(conn, "$"+strconv.Itoa(len(rawTasks))+"\r\n")
						_, _ = conn.Write(rawTasks)
						_, _ = io.WriteString(conn, "\r\n")
					case len(args) >= 2 && strings.EqualFold(args[0], "RPUSH") && args[1] == "plugin-status":
						if writes.Add(1) <= failStatuses {
							_, _ = io.WriteString(conn, "-ERR blocked\r\n")
							continue
						}
						_, _ = io.WriteString(conn, ":1\r\n")
					default:
						_, _ = io.WriteString(conn, "+OK\r\n")
					}
				}
			}(conn)
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-serverDone
	})

	host, portText, errSplit := net.SplitHostPort(listener.Addr().String())
	if errSplit != nil {
		t.Fatalf("split listener address: %v", errSplit)
	}
	port, errPort := strconv.Atoi(portText)
	if errPort != nil {
		t.Fatalf("parse port: %v", errPort)
	}
	client := home.New(config.HomeConfig{Enabled: true, Host: host, Port: port})
	t.Cleanup(client.Close)
	return client, &writes
}

func TestStageHomeOverlayDoesNotApplyConfigAfterStageFailure(t *testing.T) {
	baseCfg := &config.Config{}
	baseCfg.Home.Enabled = true
	baseCfg.Routing.Strategy = "round-robin"
	remoteCfg := &config.Config{}
	remoteCfg.Home.Enabled = true
	remoteCfg.Routing.Strategy = "fill-first"
	remoteCfg.Plugins.Enabled = true
	service := &Service{
		cfg: baseCfg,
		homePluginSyncFetch: func(context.Context, sdkpluginstore.PluginSyncRequest) (sdkpluginstore.PluginSyncResponse, error) {
			return sdkpluginstore.PluginSyncResponse{}, errors.New("plugin sync unavailable")
		},
	}

	if _, errStage := service.stageHomeOverlayWithClient(context.Background(), remoteCfg, nil); errStage == nil {
		t.Fatal("stageHomeOverlayWithClient() error = nil, want plugin sync failure")
	}

	service.cfgMu.RLock()
	strategy := service.cfg.Routing.Strategy
	service.cfgMu.RUnlock()
	if strategy != "round-robin" {
		t.Fatalf("failed stage applied routing strategy %q", strategy)
	}
}

func TestReadyHomePluginFinalizationRetriesUntilStatusSucceeds(t *testing.T) {
	client, writes := newHomePluginTaskTestClient(t, nil, 1)
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	cfg.Home.NodeID = "node-1"
	service := &Service{homeGeneration: 1}
	work := &homePluginFinalization{
		statusWork: []homePluginStatusWork{{cfg: cfg, report: homeplugins.CompletedSyncReport(homeplugins.CurrentPlatform(), nil)}},
		syncKey:    "sync-key",
		markSynced: true,
	}

	if errFinalize := service.finalizeHomePluginWorkUntilDone(context.Background(), context.Background(), 1, client, work, nil); errFinalize != nil {
		t.Fatalf("finalizeHomePluginWorkUntilDone() error = %v", errFinalize)
	}
	if gotWrites := writes.Load(); gotWrites != 2 {
		t.Fatalf("plugin status writes = %d, want 2 after retry", gotWrites)
	}
	if service.homePluginSyncKey != "sync-key" || work.nextStatus != 1 || work.markSynced {
		t.Fatalf("retried ready finalization state: key=%q next=%d marked=%v", service.homePluginSyncKey, work.nextStatus, work.markSynced)
	}
}

func TestReplacementWaitsForHomePluginFinalizationOwnership(t *testing.T) {
	client, statusStarted, releaseStatus := newBlockingHomePluginStatusClient(t)
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	cfg.Home.NodeID = "node-1"
	parentCtx, cancelParent := context.WithCancel(context.Background())
	t.Cleanup(cancelParent)
	homeCtx, cancelHome := context.WithCancel(parentCtx)
	t.Cleanup(cancelHome)
	lifetimeCtx, cancelLifetime := context.WithCancel(homeCtx)
	t.Cleanup(cancelLifetime)
	previousDone := make(chan struct{})
	cancelled := make(chan struct{})
	service := &Service{
		cfg:            cfg,
		homeGeneration: 1,
		homeSupervisor: &homeSubscriberSupervisor{cancel: func() {
			cancelLifetime()
			close(cancelled)
			close(previousDone)
		}, done: previousDone},
	}
	work := &homePluginFinalization{statusWork: []homePluginStatusWork{{cfg: cfg, report: homeplugins.CompletedSyncReport(homeplugins.CurrentPlatform(), nil)}}}
	finalized := make(chan error, 1)
	go func() {
		finalized <- service.finalizeHomePluginWorkUntilDone(lifetimeCtx, homeCtx, 1, client, work, func() bool { return true })
	}()
	select {
	case <-statusStarted:
	case <-time.After(time.Second):
		t.Fatal("plugin status finalization did not start")
	}

	replacementReturned := make(chan struct{})
	go func() {
		service.startHomeSubscriber(parentCtx)
		close(replacementReturned)
	}()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("replacement did not cancel the blocked controlled finalization")
	}
	select {
	case errFinalize := <-finalized:
		if !errors.Is(errFinalize, context.Canceled) {
			t.Fatalf("finalization error = %v, want context cancellation", errFinalize)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked finalization did not exit after replacement cancellation")
	}

	close(releaseStatus)
	cancelParent()
	select {
	case <-replacementReturned:
	case <-time.After(time.Second):
		t.Fatal("replacement did not return after cancellation")
	}
}

func TestShutdownCancelsBlockedHomePluginFinalization(t *testing.T) {
	client, statusStarted, releaseStatus := newBlockingHomePluginStatusClient(t)
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	cfg.Home.NodeID = "node-1"
	parentCtx, cancelParent := context.WithCancel(context.Background())
	t.Cleanup(cancelParent)
	homeCtx, cancelHome := context.WithCancel(parentCtx)
	t.Cleanup(cancelHome)
	lifetimeCtx, cancelLifetime := context.WithCancel(homeCtx)
	t.Cleanup(cancelLifetime)
	previousDone := make(chan struct{})
	cancelled := make(chan struct{})
	service := &Service{
		cfg:            cfg,
		homeGeneration: 1,
		homeSupervisor: &homeSubscriberSupervisor{cancel: func() {
			cancelLifetime()
			close(cancelled)
			close(previousDone)
		}, done: previousDone},
	}
	work := &homePluginFinalization{statusWork: []homePluginStatusWork{{cfg: cfg, report: homeplugins.CompletedSyncReport(homeplugins.CurrentPlatform(), nil)}}}
	finalized := make(chan error, 1)
	go func() {
		finalized <- service.finalizeHomePluginWorkUntilDone(lifetimeCtx, homeCtx, 1, client, work, nil)
	}()
	select {
	case <-statusStarted:
	case <-time.After(time.Second):
		t.Fatal("plugin status finalization did not start")
	}

	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- service.Shutdown(context.Background())
	}()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel the blocked controlled finalization")
	}
	select {
	case errFinalize := <-finalized:
		if !errors.Is(errFinalize, context.Canceled) {
			t.Fatalf("finalization error = %v, want context cancellation", errFinalize)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked finalization did not exit after shutdown cancellation")
	}

	close(releaseStatus)
	select {
	case errShutdown := <-shutdownDone:
		if errShutdown != nil {
			t.Fatalf("Shutdown() error = %v", errShutdown)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not return after finalization cancellation")
	}
}

func newBlockingHomePluginStatusClient(t *testing.T) (*home.Client, <-chan struct{}, chan<- struct{}) {
	t.Helper()
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	statusStarted := make(chan struct{})
	releaseStatus := make(chan struct{})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				reader := bufio.NewReader(conn)
				for {
					args, errRead := readRegistryTestRedisCommand(reader)
					if errRead != nil {
						return
					}
					switch {
					case len(args) > 0 && strings.EqualFold(args[0], "HELLO"):
						_, _ = io.WriteString(conn, "%6\r\n$6\r\nserver\r\n$5\r\nredis\r\n$5\r\nproto\r\n:3\r\n$2\r\nid\r\n:1\r\n$4\r\nmode\r\n$10\r\nstandalone\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$7\r\nmodules\r\n*0\r\n")
					case len(args) >= 2 && strings.EqualFold(args[0], "RPUSH") && args[1] == "plugin-status":
						close(statusStarted)
						<-releaseStatus
						_, _ = io.WriteString(conn, ":1\r\n")
					default:
						_, _ = io.WriteString(conn, "+OK\r\n")
					}
				}
			}(conn)
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-serverDone
	})
	host, portText, errSplit := net.SplitHostPort(listener.Addr().String())
	if errSplit != nil {
		t.Fatalf("split listener address: %v", errSplit)
	}
	port, errPort := strconv.Atoi(portText)
	if errPort != nil {
		t.Fatalf("parse port: %v", errPort)
	}
	client := home.New(config.HomeConfig{Enabled: true, Host: host, Port: port})
	t.Cleanup(client.Close)
	return client, statusStarted, releaseStatus
}

func TestHomePluginSyncKeyIncludesCredentialRevision(t *testing.T) {
	cfg := &config.Config{}
	cfg.Home.Enabled = true
	cfg.Plugins.Enabled = true
	cfg.Plugins.Configs = map[string]config.PluginInstanceConfig{}
	first := homePluginSyncKey(cfg)
	cfg.Plugins.AuthRevision = 2
	second := homePluginSyncKey(cfg)
	if first == second {
		t.Fatalf("homePluginSyncKey() unchanged after sync revision update: %q", first)
	}
}

func TestForceHomeRuntimeConfigClearsStoreAuth(t *testing.T) {
	cfg := &config.Config{}
	cfg.Plugins.StoreAuth = []sdkpluginstore.AuthConfig{{
		Match: "https://downloads.example/", Type: sdkpluginstore.AuthTypeBearer, TokenEnv: "PLUGIN_TOKEN",
	}}
	forceHomeRuntimeConfig(cfg)
	if cfg.Plugins.StoreAuth != nil {
		t.Fatalf("Plugins.StoreAuth = %#v, want nil in Home mode", cfg.Plugins.StoreAuth)
	}
}

func TestStageHomeOverlay_InstalledPluginAtRuntime_Issue6225(t *testing.T) {
	pluginsDir := t.TempDir()
	platform := homeplugins.CurrentPlatform()
	ext := ".so"
	switch platform.GOOS {
	case "darwin":
		ext = ".dylib"
	case "windows":
		ext = ".dll"
	}
	pluginFileName := "myplugin-v1.0.0" + ext
	if errWrite := os.WriteFile(filepath.Join(pluginsDir, pluginFileName), []byte("dummy"), 0o755); errWrite != nil {
		t.Fatalf("write dummy plugin file: %v", errWrite)
	}

	baseCfg := &config.Config{}
	baseCfg.Home.Enabled = true
	baseCfg.Home.NodeID = "node-1"
	baseCfg.Plugins.Enabled = true
	baseCfg.Plugins.Dir = pluginsDir
	baseCfg.Plugins.Configs = map[string]config.PluginInstanceConfig{}

	enabled := true
	remoteCfg := &config.Config{}
	remoteCfg.Home.Enabled = true
	remoteCfg.Home.NodeID = "node-1"
	remoteCfg.Plugins.Enabled = true
	remoteCfg.Plugins.Dir = pluginsDir
	remoteCfg.Plugins.Configs = map[string]config.PluginInstanceConfig{
		"myplugin": {Enabled: &enabled},
	}

	client, writes := newHomePluginTaskTestClient(t, nil, 0)

	service := &Service{
		cfg:        baseCfg,
		pluginHost: pluginhost.New(),
		homePluginSyncFetch: func(context.Context, sdkpluginstore.PluginSyncRequest) (sdkpluginstore.PluginSyncResponse, error) {
			return sdkpluginstore.PluginSyncResponse{
				SchemaVersion: sdkpluginstore.PluginSyncSchemaVersion,
				ExpiresAt:     time.Now().UTC().Add(time.Minute),
				Items:         []sdkpluginstore.PluginSyncItem{},
			}, nil
		},
	}

	work, errStage := service.stageHomeOverlayWithClient(context.Background(), remoteCfg, client)
	if errStage != nil {
		t.Fatalf("stageHomeOverlayWithClient() error = %v, want successful stage without premature load failure", errStage)
	}
	if work == nil {
		t.Fatal("stageHomeOverlayWithClient() returned nil work")
	}
	if len(work.statusWork) != 1 {
		t.Fatalf("len(work.statusWork) = %d, want 1", len(work.statusWork))
	}
	if !work.statusWork[0].needsLoadMarking {
		t.Fatal("statusWork[0].needsLoadMarking = false, want true")
	}

	if errFinalize := service.finalizeHomePluginWork(context.Background(), client, work); errFinalize != nil {
		t.Fatalf("finalizeHomePluginWork() error = %v", errFinalize)
	}
	if writes.Load() != 1 {
		t.Fatalf("plugin status writes = %d, want 1", writes.Load())
	}
	if work.statusWork[0].needsLoadMarking {
		t.Fatal("statusWork[0].needsLoadMarking = true after finalize, want false")
	}
	if gotLoadStatus := work.statusWork[0].report.Plugins[0].LoadStatus; gotLoadStatus != "failed" {
		t.Fatalf("LoadStatus = %q, want 'failed' for unapplied plugin", gotLoadStatus)
	}
}

func TestHomeConfigWorkQueue_TryDequeueLatest_Issue6225(t *testing.T) {
	q := newHomeConfigWorkQueue()
	if _, ok := q.tryDequeueLatest(); ok {
		t.Fatal("tryDequeueLatest() on empty queue = true, want false")
	}

	q.enqueue([]byte("payload-1"))
	q.enqueue([]byte("payload-2"))
	q.enqueue([]byte("payload-3"))

	latest, ok := q.tryDequeueLatest()
	if !ok {
		t.Fatal("tryDequeueLatest() = false, want true")
	}
	if string(latest) != "payload-3" {
		t.Fatalf("tryDequeueLatest() = %q, want payload-3", string(latest))
	}

	if _, ok := q.tryDequeueLatest(); ok {
		t.Fatal("tryDequeueLatest() after draining = true, want false")
	}
}

func TestHomeConfigWorkerPreemptsFailingConfigWithNewerPayload_Issue6225(t *testing.T) {
	client, _ := newHomePluginTaskTestClient(t, nil, 0)
	baseCfg := &config.Config{}
	baseCfg.Home.Enabled = true
	baseCfg.Routing.Strategy = "initial"

	service := &Service{
		cfg:            baseCfg,
		homeGeneration: 1,
		homePluginSyncFetch: func(_ context.Context, _ sdkpluginstore.PluginSyncRequest) (sdkpluginstore.PluginSyncResponse, error) {
			return sdkpluginstore.PluginSyncResponse{
				SchemaVersion: sdkpluginstore.PluginSyncSchemaVersion,
				ExpiresAt:     time.Now().Add(time.Minute),
			}, nil
		},
	}

	queue := newHomeConfigWorkQueue()
	// Enqueue an invalid payload that fails config parsing and triggers the retry loop
	queue.enqueue([]byte("invalid: yaml: ["))
	// Enqueue a valid recovered config payload that should preempt the failing one during retry
	queue.enqueue([]byte("plugins:\n  enabled: true\nrouting:\n  strategy: recovered\n"))

	ready := make(chan struct{})
	close(ready)
	lifetimeCtx, cancelLifetime := context.WithCancel(context.Background())
	workerDone := make(chan struct{})
	t.Cleanup(func() {
		cancelLifetime()
		select {
		case <-workerDone:
		case <-time.After(time.Second):
		}
	})

	cancelBound := atomic.Int64{}
	cancelBound.Store(int64(time.Second))
	published := atomic.Bool{}
	published.Store(true)

	go func() {
		defer close(workerDone)
		service.runHomeConfigWorker(lifetimeCtx, context.Background(), 1, client, executionregistry.New(), queue, ready, &published, &cancelBound)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		service.cfgMu.RLock()
		strat := service.cfg.Routing.Strategy
		service.cfgMu.RUnlock()
		if strat == "recovered" {
			return
		}
		pollTimer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-lifetimeCtx.Done():
			pollTimer.Stop()
			return
		case <-pollTimer.C:
		}
	}

	service.cfgMu.RLock()
	currentStrat := service.cfg.Routing.Strategy
	service.cfgMu.RUnlock()
	t.Fatalf("worker never recovered to strategy 'recovered', current=%q", currentStrat)
}
