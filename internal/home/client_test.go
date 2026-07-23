package home

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginstore"
)

func TestAuthDispatchRequestIncludesCount(t *testing.T) {
	req := newAuthDispatchRequest("gpt-5.4", "session-1", http.Header{"Authorization": {"Bearer test"}}, 2)

	raw, err := json.Marshal(&req)
	if err != nil {
		t.Fatalf("marshal auth dispatch request: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal auth dispatch request: %v", err)
	}
	if got := int(payload["count"].(float64)); got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
	if got := int(payload["concurrency_protocol"].(float64)); got != 1 {
		t.Fatalf("concurrency_protocol = %d, want 1", got)
	}
}

func TestAuthDispatchRequestDefaultsCountToOne(t *testing.T) {
	req := newAuthDispatchRequest("gpt-5.4", "", nil, 0)

	if req.Count != 1 {
		t.Fatalf("count = %d, want 1", req.Count)
	}
}

func TestRedisOptionsHomeTLSDisabled(t *testing.T) {
	client := New(config.HomeConfig{
		Enabled: true,
		Host:    "127.0.0.1",
		Port:    6379,
	})

	client.mu.Lock()
	options, err := client.redisOptionsLocked("127.0.0.1:6379")
	client.mu.Unlock()
	if err != nil {
		t.Fatalf("redisOptionsLocked() error = %v", err)
	}

	if options.TLSConfig != nil {
		t.Fatalf("TLSConfig = %#v, want nil", options.TLSConfig)
	}
	if options.Password != "" {
		t.Fatalf("Password = %q, want empty", options.Password)
	}
}

func TestRedisOptionsHomeTLSEnabledUsesSeedHostAsServerName(t *testing.T) {
	client := New(config.HomeConfig{
		Enabled: true,
		Host:    "home.example.com",
		Port:    444,
		TLS: config.HomeTLSConfig{
			Enable: true,
		},
	})
	client.homeCfg.Host = "127.0.0.1"

	client.mu.Lock()
	options, err := client.redisOptionsLocked("127.0.0.1:444")
	client.mu.Unlock()
	if err != nil {
		t.Fatalf("redisOptionsLocked() error = %v", err)
	}

	if options.TLSConfig == nil {
		t.Fatal("TLSConfig is nil")
	}
	if options.TLSConfig.ServerName != "home.example.com" {
		t.Fatalf("ServerName = %q, want home.example.com", options.TLSConfig.ServerName)
	}
	if options.TLSConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %d, want TLS 1.2", options.TLSConfig.MinVersion)
	}
}

func TestRedisOptionsHomeTLSEnabledUsesExplicitServerName(t *testing.T) {
	client := New(config.HomeConfig{
		Enabled: true,
		Host:    "127.0.0.1",
		Port:    444,
		TLS: config.HomeTLSConfig{
			Enable:             true,
			ServerName:         "home.example.com",
			InsecureSkipVerify: true,
		},
	})

	client.mu.Lock()
	options, err := client.redisOptionsLocked("127.0.0.1:444")
	client.mu.Unlock()
	if err != nil {
		t.Fatalf("redisOptionsLocked() error = %v", err)
	}

	if options.TLSConfig == nil {
		t.Fatal("TLSConfig is nil")
	}
	if options.TLSConfig.ServerName != "home.example.com" {
		t.Fatalf("ServerName = %q, want home.example.com", options.TLSConfig.ServerName)
	}
	if !options.TLSConfig.InsecureSkipVerify {
		t.Fatal("InsecureSkipVerify = false, want true")
	}
}

func TestRefreshClusterNodesDisabledSkipsRedisCommand(t *testing.T) {
	client := New(config.HomeConfig{
		Enabled:                 true,
		Host:                    "127.0.0.1",
		Port:                    1,
		DisableClusterDiscovery: true,
	})

	switched, err := client.refreshClusterNodes(context.Background())
	if err != nil {
		t.Fatalf("refreshClusterNodes() error = %v", err)
	}
	if switched {
		t.Fatal("refreshClusterNodes() switched = true, want false")
	}
	if client.cmd != nil || client.sub != nil {
		t.Fatalf("redis clients were initialized when cluster discovery was disabled")
	}
}

func TestFailoverAfterReconnectFailureDisabledDoesNotSwitchToClusterNode(t *testing.T) {
	client := New(config.HomeConfig{
		Enabled:                 true,
		Host:                    "seed.example.com",
		Port:                    8327,
		DisableClusterDiscovery: true,
	})
	client.mu.Lock()
	client.clusterNodes = []clusterNode{{IP: "other.example.com", Port: 8327}}
	client.reconnectFailures = homeReconnectFailoverThreshold - 1
	client.mu.Unlock()

	switched, addr := client.failoverAfterReconnectFailure()
	if switched {
		t.Fatalf("failoverAfterReconnectFailure() switched to %s, want no switch", addr)
	}
	if got, _ := client.addr(); got != "seed.example.com:8327" {
		t.Fatalf("addr() = %q, want seed.example.com:8327", got)
	}
}

func TestBuildKVSetArgs(t *testing.T) {
	args, errArgs := buildKVSetArgs("key", []byte("value"), KVSetOptions{EX: 2 * time.Second, NX: true})
	if errArgs != nil {
		t.Fatalf("buildKVSetArgs(EX NX) error = %v", errArgs)
	}
	want := []any{"key", []byte("value"), "EX", int64(2), "NX"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("buildKVSetArgs(EX NX) = %#v, want %#v", args, want)
	}

	args, errArgs = buildKVSetArgs("key", []byte("value"), KVSetOptions{PX: 1500 * time.Millisecond, XX: true})
	if errArgs != nil {
		t.Fatalf("buildKVSetArgs(PX XX) error = %v", errArgs)
	}
	want = []any{"key", []byte("value"), "PX", int64(1500), "XX"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("buildKVSetArgs(PX XX) = %#v, want %#v", args, want)
	}

	if _, errConflict := buildKVSetArgs("key", []byte("value"), KVSetOptions{EX: time.Second, PX: time.Millisecond}); errConflict == nil {
		t.Fatalf("buildKVSetArgs(EX PX) error = nil, want error")
	}
	if _, errConflict := buildKVSetArgs("key", []byte("value"), KVSetOptions{NX: true, XX: true}); errConflict == nil {
		t.Fatalf("buildKVSetArgs(NX XX) error = nil, want error")
	}
}

func TestClientLPushInFlightSnapshotUsesDedicatedKeyWithoutChangingHeartbeat(t *testing.T) {
	client, commands := newRedisCommandTestClient(t, func(args []string) string {
		if len(args) > 0 && strings.EqualFold(args[0], "LPUSH") {
			return ":1\r\n"
		}
		return "-ERR unexpected command\r\n"
	})
	client.heartbeatOK.Store(true)

	if errPush := client.LPushInFlightSnapshot(context.Background(), []byte(`{"revision":1}`)); errPush != nil {
		t.Fatalf("LPushInFlightSnapshot() error = %v", errPush)
	}
	if !client.HeartbeatOK() {
		t.Fatal("LPushInFlightSnapshot() changed heartbeat state")
	}
	last := commands.Last()
	if len(last) != 3 || !strings.EqualFold(last[0], "LPUSH") || last[1] != redisKeyInFlightSnapshot || last[2] != `{"revision":1}` {
		t.Fatalf("LPushInFlightSnapshot() command = %#v", last)
	}
}

func TestClientPushConcurrencyReleaseUsesIndependentClient(t *testing.T) {
	client, commands := newRedisCommandTestClient(t, func(args []string) string {
		if len(args) > 0 && strings.EqualFold(args[0], "LPUSH") {
			return ":1\r\n"
		}
		return "-ERR unexpected command\r\n"
	})
	commandClient := client.cmd

	frame := concurrencyReleaseFrameFromFixture(t)
	if errPush := client.PushConcurrencyRelease(context.Background(), frame); errPush != nil {
		t.Fatalf("PushConcurrencyRelease() error = %v", errPush)
	}
	if client.release == nil || client.release == commandClient {
		t.Fatal("PushConcurrencyRelease() did not create an independent client")
	}
	last := commands.Last()
	if want := []string{"LPUSH", redisKeyConcurrencyRelease, `{"credential_id":"cred-1","model":"gpt","release_seq":1}`}; !reflect.DeepEqual(last, want) {
		t.Fatalf("PushConcurrencyRelease() command = %#v, want %#v", last, want)
	}
}

func TestClientLPushInFlightSnapshotErrorKeepsHeartbeat(t *testing.T) {
	client, _ := newRedisCommandTestClient(t, func(args []string) string {
		if len(args) > 0 && strings.EqualFold(args[0], "LPUSH") {
			return "-ERR unavailable\r\n"
		}
		return "-ERR unexpected command\r\n"
	})
	client.heartbeatOK.Store(true)

	if errPush := client.LPushInFlightSnapshot(context.Background(), []byte(`{"revision":1}`)); errPush == nil {
		t.Fatal("LPushInFlightSnapshot() error = nil")
	}
	if !client.HeartbeatOK() {
		t.Fatal("LPushInFlightSnapshot() changed heartbeat state after an error")
	}
}

func TestKVGetConvertsRedisNilToMiss(t *testing.T) {
	client, _ := newRedisCommandTestClient(t, func(args []string) string {
		if len(args) > 0 && strings.EqualFold(args[0], "GET") {
			return "$-1\r\n"
		}
		return "-ERR unexpected command\r\n"
	})

	value, found, errGet := client.KVGet(context.Background(), "missing")
	if errGet != nil {
		t.Fatalf("KVGet() error = %v", errGet)
	}
	if found || value != nil {
		t.Fatalf("KVGet() = %v, %v, want nil, false", value, found)
	}
}

func TestKVMGetConvertsNilItemsToMiss(t *testing.T) {
	client, _ := newRedisCommandTestClient(t, func(args []string) string {
		if len(args) > 0 && strings.EqualFold(args[0], "MGET") {
			return "*2\r\n$5\r\nvalue\r\n$-1\r\n"
		}
		return "-ERR unexpected command\r\n"
	})

	values, found, errMGet := client.KVMGet(context.Background(), "hit", "miss")
	if errMGet != nil {
		t.Fatalf("KVMGet() error = %v", errMGet)
	}
	if len(values) != 2 || len(found) != 2 {
		t.Fatalf("KVMGet() lengths = %d, %d, want 2, 2", len(values), len(found))
	}
	if !found[0] || string(values[0]) != "value" {
		t.Fatalf("KVMGet()[0] = %q, %v, want value, true", values[0], found[0])
	}
	if found[1] || values[1] != nil {
		t.Fatalf("KVMGet()[1] = %v, %v, want nil, false", values[1], found[1])
	}
}

func TestKVSetConditionUnmetReturnsFalse(t *testing.T) {
	client, _ := newRedisCommandTestClient(t, func(args []string) string {
		if len(args) > 0 && strings.EqualFold(args[0], "SET") {
			return "$-1\r\n"
		}
		return "-ERR unexpected command\r\n"
	})

	written, errSet := client.KVSet(context.Background(), "key", []byte("value"), KVSetOptions{NX: true})
	if errSet != nil {
		t.Fatalf("KVSet() error = %v", errSet)
	}
	if written {
		t.Fatalf("KVSet() written = true, want false")
	}
}

func TestKVCompareAndSwapReturnsScriptResult(t *testing.T) {
	client, commands := newRedisCommandTestClient(t, func(args []string) string {
		if len(args) > 0 && strings.EqualFold(args[0], "EVAL") {
			return ":1\r\n"
		}
		return "-ERR unexpected command\r\n"
	})

	swapped, errCAS := client.KVCompareAndSwap(context.Background(), "key", []byte("old"), true, []byte("new"), 1500*time.Millisecond)
	if errCAS != nil {
		t.Fatalf("KVCompareAndSwap() error = %v", errCAS)
	}
	if !swapped {
		t.Fatal("KVCompareAndSwap() swapped = false, want true")
	}
	if lastCommand := commands.Last(); len(lastCommand) < 2 || !strings.EqualFold(lastCommand[0], "EVAL") {
		t.Fatalf("last command = %#v, want EVAL", lastCommand)
	}
}

func TestKVMSetUsesStableKeyOrder(t *testing.T) {
	client, commands := newRedisCommandTestClient(t, func(args []string) string {
		if len(args) > 0 && strings.EqualFold(args[0], "MSET") {
			return "+OK\r\n"
		}
		return "-ERR unexpected command\r\n"
	})

	if errMSet := client.KVMSet(context.Background(), map[string][]byte{
		"b": []byte("2"),
		"a": []byte("1"),
	}); errMSet != nil {
		t.Fatalf("KVMSet() error = %v", errMSet)
	}
	got := commands.Last()
	want := []string{"MSET", "a", "1", "b", "2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MSET command = %#v, want %#v", got, want)
	}
}

func TestRPushPluginStatusUsesPluginStatusKey(t *testing.T) {
	client, commands := newRedisCommandTestClient(t, func(args []string) string {
		if len(args) > 0 && strings.EqualFold(args[0], "RPUSH") {
			return ":1\r\n"
		}
		return "-ERR unexpected command\r\n"
	})

	if errPush := client.RPushPluginStatus(context.Background(), []byte(`{"ok":true}`)); errPush != nil {
		t.Fatalf("RPushPluginStatus() error = %v", errPush)
	}
	got := commands.Last()
	want := []string{"rpush", "plugin-status", `{"ok":true}`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RPUSH command = %#v, want %#v", got, want)
	}
}

func TestGetPluginTasksUsesPluginTasksKey(t *testing.T) {
	client, commands := newRedisCommandTestClient(t, func(args []string) string {
		if len(args) > 0 && strings.EqualFold(args[0], "GET") {
			payload := `[{"id":7,"operation":"delete","plugin_id":"sample"}]`
			return fmt.Sprintf("$%d\r\n%s\r\n", len(payload), payload)
		}
		return "-ERR unexpected command\r\n"
	})

	tasks, errTasks := client.GetPluginTasks(context.Background())
	if errTasks != nil {
		t.Fatalf("GetPluginTasks() error = %v", errTasks)
	}
	if len(tasks) != 1 || tasks[0].ID != 7 || tasks[0].Operation != "delete" || tasks[0].PluginID != "sample" {
		t.Fatalf("tasks = %+v, want one delete task", tasks)
	}
	got := commands.Last()
	want := []string{"get", "plugin-tasks"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GET command = %#v, want %#v", got, want)
	}
}

func TestPluginSyncCommandClientUsesDedicatedTimeout(t *testing.T) {
	template := &redis.Options{
		Addr:         "127.0.0.1:1",
		ReadTimeout:  homeRedisOperationTimeout,
		WriteTimeout: homeRedisOperationTimeout,
		MaxRetries:   -1,
	}
	pluginSync := newPluginSyncCommandClient(context.Background(), template)
	if pluginSync == nil {
		t.Fatal("newPluginSyncCommandClient() = nil")
	}
	t.Cleanup(func() { _ = pluginSync.Close() })
	if pluginSync.Options().ReadTimeout != homePluginSyncOperationTimeout || pluginSync.Options().WriteTimeout != homeRedisOperationTimeout {
		t.Fatalf("plugin sync timeouts = %s/%s, want %s/%s", pluginSync.Options().ReadTimeout, pluginSync.Options().WriteTimeout, homePluginSyncOperationTimeout, homeRedisOperationTimeout)
	}
	if template.ReadTimeout != homeRedisOperationTimeout || template.WriteTimeout != homeRedisOperationTimeout || template.MaxRetries != -1 {
		t.Fatalf("template options were mutated: read=%s write=%s retries=%d", template.ReadTimeout, template.WriteTimeout, template.MaxRetries)
	}
}

func TestGetPluginSyncUsesDedicatedCommandAndDecodesResponse(t *testing.T) {
	response := pluginstore.PluginSyncResponse{
		SchemaVersion: pluginstore.PluginSyncSchemaVersion,
		ExpiresAt:     time.Now().UTC().Add(time.Minute),
		Items: []pluginstore.PluginSyncItem{{
			Manifest: pluginstore.Manifest{
				SchemaVersion: pluginstore.SchemaVersionV2,
				ID:            "sample",
				Version:       "1.0.0",
				Install: pluginstore.InstallPlan{Type: pluginstore.InstallTypeDirect, Artifacts: []pluginstore.Artifact{{
					GOOS: "linux", GOARCH: "amd64", URL: "https://downloads.example/sample.zip",
					SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
				}}},
			},
			Auth: []pluginstore.ResolvedAuthConfig{{
				Match: "https://downloads.example/", Type: pluginstore.AuthTypeBearer, Token: pluginstore.Secret("temporary-token"),
			}},
		}},
	}
	payload, errMarshal := json.Marshal(response)
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}
	client, commands := newRedisCommandTestClient(t, func(args []string) string {
		if len(args) > 0 && strings.EqualFold(args[0], "GET") {
			return fmt.Sprintf("$%d\r\n%s\r\n", len(payload), payload)
		}
		return "-ERR unexpected command\r\n"
	})
	request := pluginstore.PluginSyncRequest{
		SchemaVersion: pluginstore.PluginSyncSchemaVersion,
		GOOS:          "linux",
		GOARCH:        "amd64",
		InstalledVersions: map[string]string{
			"sample": "0.9.0",
		},
	}

	gotResponse, errSync := client.GetPluginSync(context.Background(), request)
	if errSync != nil {
		t.Fatalf("GetPluginSync() error = %v", errSync)
	}
	defer gotResponse.Clear()
	if len(gotResponse.Items) != 1 || string(gotResponse.Items[0].Auth[0].Token) != "temporary-token" {
		t.Fatalf("response = %#v, want one item with temporary token", gotResponse)
	}
	got := commands.Last()
	if len(got) != 3 || !strings.EqualFold(got[0], "get") || got[1] != "plugin-sync" {
		t.Fatalf("plugin sync command = %#v, want GET plugin-sync <request>", got)
	}
	var gotRequest pluginstore.PluginSyncRequest
	if errUnmarshal := json.Unmarshal([]byte(got[2]), &gotRequest); errUnmarshal != nil {
		t.Fatalf("decode request command: %v", errUnmarshal)
	}
	if gotRequest.InstalledVersions["sample"] != "0.9.0" {
		t.Fatalf("request = %#v, want installed sample 0.9.0", gotRequest)
	}
}

func TestGetPluginSyncExceedsBaseTimeoutAndKeepsBaseClientUsable(t *testing.T) {
	response := pluginstore.PluginSyncResponse{
		SchemaVersion: pluginstore.PluginSyncSchemaVersion,
		ExpiresAt:     time.Now().UTC().Add(time.Minute),
		Items:         []pluginstore.PluginSyncItem{},
	}
	payload, errMarshal := json.Marshal(response)
	if errMarshal != nil {
		t.Fatalf("Marshal() error = %v", errMarshal)
	}
	client, _ := newRedisCommandTestClient(t, func(args []string) string {
		if len(args) < 2 || !strings.EqualFold(args[0], "GET") {
			return "-ERR unexpected command\r\n"
		}
		switch args[1] {
		case redisKeyPluginSync:
			time.Sleep(3 * homeRedisTestOperationTimeout)
			return fmt.Sprintf("$%d\r\n%s\r\n", len(payload), payload)
		case redisKeyPluginTasks:
			return "$2\r\n[]\r\n"
		default:
			return "-ERR unexpected key\r\n"
		}
	})
	startedAt := time.Now()
	got, errSync := client.GetPluginSync(context.Background(), pluginstore.PluginSyncRequest{
		SchemaVersion: pluginstore.PluginSyncSchemaVersion, GOOS: "linux", GOARCH: "amd64",
	})
	if errSync != nil {
		t.Fatalf("GetPluginSync() error = %v", errSync)
	}
	got.Clear()
	if elapsed := time.Since(startedAt); elapsed < 2*homeRedisTestOperationTimeout {
		t.Fatalf("GetPluginSync() elapsed = %s, want response beyond base timeout", elapsed)
	}
	if _, errTasks := client.GetPluginTasks(context.Background()); errTasks != nil {
		t.Fatalf("GetPluginTasks() after plugin sync error = %v", errTasks)
	}
}

func TestGetPluginSyncCancellationInterruptsRead(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	client, commands := newRedisCommandTestClient(t, func(args []string) string {
		if len(args) >= 2 && args[1] == redisKeyPluginSync {
			startOnce.Do(func() { close(started) })
			<-release
		}
		return "-ERR cancelled\r\n"
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()
	startedAt := time.Now()
	_, errSync := client.GetPluginSync(ctx, pluginstore.PluginSyncRequest{
		SchemaVersion: pluginstore.PluginSyncSchemaVersion, GOOS: "linux", GOARCH: "amd64",
	})
	close(release)
	if !errors.Is(errSync, context.Canceled) {
		t.Fatalf("GetPluginSync() error = %v, want context.Canceled", errSync)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("GetPluginSync() cancellation took %s", elapsed)
	}
	if count := commands.CountKey(redisKeyPluginSync); count != 1 {
		t.Fatalf("plugin sync command count = %d, want 1", count)
	}
}

func TestProcessPluginSyncCommandCancellationInterruptsTLSHandshake(t *testing.T) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	defer func() { _ = listener.Close() }()
	accepted := make(chan struct{})
	release := make(chan struct{})
	serverDone := make(chan error, 1)
	go func() {
		conn, errAccept := listener.Accept()
		if errAccept != nil {
			serverDone <- errAccept
			return
		}
		close(accepted)
		<-release
		serverDone <- conn.Close()
	}()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-accepted
		cancel()
	}()
	options := &redis.Options{
		Addr:                  listener.Addr().String(),
		TLSConfig:             &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}, //nolint:gosec -- the test peer intentionally never completes TLS.
		DialTimeout:           time.Second,
		ReadTimeout:           homeRedisTestOperationTimeout,
		WriteTimeout:          homeRedisTestOperationTimeout,
		MaxRetries:            -1,
		ContextTimeoutEnabled: true,
	}
	command := redis.NewStringCmd(ctx, "get", redisKeyPluginSync, `{}`)
	startedAt := time.Now()
	errProcess := processPluginSyncCommand(ctx, options, command)
	close(release)
	if errServer := <-serverDone; errServer != nil {
		t.Fatalf("server close error = %v", errServer)
	}
	if !errors.Is(errProcess, context.Canceled) {
		t.Fatalf("processPluginSyncCommand() error = %v, want context.Canceled", errProcess)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("TLS handshake cancellation took %s", elapsed)
	}
}

func TestGetPluginTasksRetainsBaseTimeout(t *testing.T) {
	client, _ := newRedisCommandTestClient(t, func(args []string) string {
		if len(args) >= 2 && args[1] == redisKeyPluginTasks {
			time.Sleep(3 * homeRedisTestOperationTimeout)
			return "$2\r\n[]\r\n"
		}
		return "-ERR unexpected command\r\n"
	})
	if _, errTasks := client.GetPluginTasks(context.Background()); errTasks == nil {
		t.Fatal("GetPluginTasks() error = nil, want base read timeout")
	}
}

func TestGetPluginSyncRecognizesUnsupportedHomeProtocol(t *testing.T) {
	tests := []struct {
		name     string
		response string
	}{
		{
			name: "legacy json error",
			response: func() string {
				payload := `{"error":{"type":"error","message":"wrong number of arguments for 'get' command"}}`
				return fmt.Sprintf("$%d\r\n%s\r\n", len(payload), payload)
			}(),
		},
		{
			name:     "redis unsupported key",
			response: "-ERR unsupported key\r\n",
		},
		{
			name: "structured unsupported type",
			response: func() string {
				payload := `{"error":{"type":"plugin_sync_unsupported","message":"plugin sync is unsupported"}}`
				return fmt.Sprintf("$%d\r\n%s\r\n", len(payload), payload)
			}(),
		},
		{
			name:     "redis unsupported code",
			response: "-ERR plugin_sync_unsupported\r\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := newRedisCommandTestClient(t, func(args []string) string {
				if len(args) > 0 && strings.EqualFold(args[0], "GET") {
					return tt.response
				}
				return "-ERR unexpected command\r\n"
			})
			_, errSync := client.GetPluginSync(context.Background(), pluginstore.PluginSyncRequest{
				SchemaVersion: pluginstore.PluginSyncSchemaVersion,
				GOOS:          "linux",
				GOARCH:        "amd64",
			})
			if !errors.Is(errSync, ErrPluginSyncUnsupported) {
				t.Fatalf("GetPluginSync() error = %v, want ErrPluginSyncUnsupported", errSync)
			}
		})
	}
}

func TestGetPluginSyncDoesNotFallbackForOtherHomeErrors(t *testing.T) {
	tests := []struct {
		name     string
		response string
	}{
		{
			name: "runtime not ready",
			response: func() string {
				payload := `{"error":{"type":"error","message":"runtime not ready"}}`
				return fmt.Sprintf("$%d\r\n%s\r\n", len(payload), payload)
			}(),
		},
		{
			name: "unsupported key substring",
			response: func() string {
				payload := `{"error":{"type":"error","message":"plugin registry contains unsupported key metadata"}}`
				return fmt.Sprintf("$%d\r\n%s\r\n", len(payload), payload)
			}(),
		},
		{
			name:     "wrong arguments substring",
			response: "-ERR failed to get plugin sync: wrong number of arguments in credential resolver\r\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := newRedisCommandTestClient(t, func(args []string) string {
				if len(args) > 0 && strings.EqualFold(args[0], "GET") {
					return tt.response
				}
				return "-ERR unexpected command\r\n"
			})

			_, errSync := client.GetPluginSync(context.Background(), pluginstore.PluginSyncRequest{
				SchemaVersion: pluginstore.PluginSyncSchemaVersion,
				GOOS:          "linux",
				GOARCH:        "amd64",
			})
			if errSync == nil {
				t.Fatal("GetPluginSync() error = nil, want plugin sync failure")
			}
			if errors.Is(errSync, ErrPluginSyncUnsupported) {
				t.Fatalf("GetPluginSync() error = %v, want no legacy fallback", errSync)
			}
		})
	}
}

type redisCommandLog struct {
	mu       sync.Mutex
	commands [][]string
}

func (l *redisCommandLog) Append(args []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.commands = append(l.commands, append([]string(nil), args...))
}

func (l *redisCommandLog) Last() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.commands) == 0 {
		return nil
	}
	return append([]string(nil), l.commands[len(l.commands)-1]...)
}

func (l *redisCommandLog) All() [][]string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([][]string, len(l.commands))
	for index := range l.commands {
		out[index] = append([]string(nil), l.commands[index]...)
	}
	return out
}

func (l *redisCommandLog) CountKey(key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	count := 0
	for _, command := range l.commands {
		if len(command) >= 2 && command[1] == key {
			count++
		}
	}
	return count
}

func (l *redisCommandLog) CountCommandKey(commandName string, key string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	count := 0
	for _, command := range l.commands {
		if len(command) >= 2 && strings.EqualFold(command[0], commandName) && command[1] == key {
			count++
		}
	}
	return count
}

const homeRedisTestOperationTimeout = 50 * time.Millisecond

func newRedisCommandTestClient(t *testing.T, handler func([]string) string) (*Client, *redisCommandLog) {
	t.Helper()

	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	log := &redisCommandLog{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			go serveRedisCommandTestConn(conn, log, handler)
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
	})

	host, portText, errSplit := net.SplitHostPort(listener.Addr().String())
	if errSplit != nil {
		t.Fatalf("split listener addr: %v", errSplit)
	}
	port, errPort := strconv.Atoi(portText)
	if errPort != nil {
		t.Fatalf("parse listener port: %v", errPort)
	}
	client := New(config.HomeConfig{
		Enabled:                 true,
		Host:                    host,
		Port:                    port,
		DisableClusterDiscovery: true,
	})
	options := &redis.Options{
		Addr:                  listener.Addr().String(),
		Protocol:              2,
		DisableIdentity:       true,
		DialTimeout:           homeRedisTestOperationTimeout,
		ReadTimeout:           homeRedisTestOperationTimeout,
		WriteTimeout:          homeRedisTestOperationTimeout,
		MaxRetries:            -1,
		ContextTimeoutEnabled: true,
	}
	client.cmdOptions = cloneRedisOptions(options)
	client.cmd = redis.NewClient(options)
	t.Cleanup(func() {
		client.Close()
	})
	return client, log
}

func newBlockingRPopTestClient(t *testing.T) (*Client, <-chan struct{}, chan struct{}) {
	t.Helper()
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	requestRead := make(chan struct{})
	release := make(chan struct{})
	serverDone := make(chan struct{})
	var handlers sync.WaitGroup
	go func() {
		defer close(serverDone)
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			handlers.Add(1)
			go func(conn net.Conn) {
				defer handlers.Done()
				defer func() { _ = conn.Close() }()
				reader := bufio.NewReader(conn)
				for {
					args, errRead := readRedisCommand(reader)
					if errRead != nil {
						return
					}
					if len(args) > 0 && strings.EqualFold(args[0], "HELLO") {
						if _, errWrite := io.WriteString(conn, "%6\r\n$6\r\nserver\r\n$5\r\nredis\r\n$5\r\nproto\r\n:3\r\n$2\r\nid\r\n:1\r\n$4\r\nmode\r\n$10\r\nstandalone\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$7\r\nmodules\r\n*0\r\n"); errWrite != nil {
							return
						}
						continue
					}
					if len(args) > 0 && strings.EqualFold(args[0], "RPOP") {
						select {
						case <-requestRead:
						default:
							close(requestRead)
						}
						<-release
						return
					}
					if _, errWrite := io.WriteString(conn, "+OK\r\n"); errWrite != nil {
						return
					}
				}
			}(conn)
		}
	}()

	options := &redis.Options{
		Addr:                  listener.Addr().String(),
		Protocol:              2,
		DisableIdentity:       true,
		DialTimeout:           time.Second,
		ReadTimeout:           time.Second,
		WriteTimeout:          time.Second,
		MaxRetries:            -1,
		ContextTimeoutEnabled: true,
	}
	client := New(config.HomeConfig{Enabled: true, Host: "127.0.0.1", Port: 1, DisableClusterDiscovery: true})
	options.Dialer = client.trackedRedisDialer(redis.NewDialer(options))
	client.cmdOptions = cloneRedisOptions(options)
	client.cmd = redis.NewClient(options)
	client.sub = redis.NewClient(cloneRedisOptions(options))
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		client.Close()
		_ = listener.Close()
		<-serverDone
		handlers.Wait()
	})
	return client, requestRead, release
}

func serveRedisCommandTestConn(conn net.Conn, log *redisCommandLog, handler func([]string) string) {
	defer func() {
		_ = conn.Close()
	}()
	reader := bufio.NewReader(conn)
	for {
		args, errRead := readRedisCommand(reader)
		if errRead != nil {
			return
		}
		log.Append(args)
		response := "+OK\r\n"
		if handler != nil {
			response = handler(args)
		}
		if _, errWrite := io.WriteString(conn, response); errWrite != nil {
			return
		}
	}
}

func readRedisCommand(reader *bufio.Reader) ([]string, error) {
	line, errRead := reader.ReadString('\n')
	if errRead != nil {
		return nil, errRead
	}
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "*") {
		return nil, fmt.Errorf("expected array, got %q", line)
	}
	count, errCount := strconv.Atoi(strings.TrimPrefix(line, "*"))
	if errCount != nil {
		return nil, errCount
	}
	args := make([]string, 0, count)
	for i := 0; i < count; i++ {
		bulkLine, errBulk := reader.ReadString('\n')
		if errBulk != nil {
			return nil, errBulk
		}
		bulkLine = strings.TrimSpace(bulkLine)
		if !strings.HasPrefix(bulkLine, "$") {
			return nil, fmt.Errorf("expected bulk string, got %q", bulkLine)
		}
		size, errSize := strconv.Atoi(strings.TrimPrefix(bulkLine, "$"))
		if errSize != nil {
			return nil, errSize
		}
		payload := make([]byte, size+2)
		if _, errFull := io.ReadFull(reader, payload); errFull != nil {
			return nil, errFull
		}
		args = append(args, string(payload[:size]))
	}
	return args, nil
}

func TestModelsRequestSerializationCarriesCredentials(t *testing.T) {
	req := modelsRequest{
		Type:    "models",
		Headers: headersToLowerMap(http.Header{"Authorization": {"Bearer test-key"}}),
		Query:   queryToLowerMap(url.Values{"key": {"gemini-key"}}),
	}

	raw, err := json.Marshal(&req)
	if err != nil {
		t.Fatalf("marshal models request: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal models request: %v", err)
	}
	if payload["type"] != "models" {
		t.Fatalf("type = %v, want models", payload["type"])
	}
	headers, ok := payload["headers"].(map[string]any)
	if !ok {
		t.Fatalf("headers missing or wrong type: %v", payload["headers"])
	}
	if headers["authorization"] != "Bearer test-key" {
		t.Fatalf("headers.authorization = %v, want Bearer test-key", headers["authorization"])
	}
	query, ok := payload["query"].(map[string]any)
	if !ok {
		t.Fatalf("query missing or wrong type: %v", payload["query"])
	}
	if query["key"] != "gemini-key" {
		t.Fatalf("query.key = %v, want gemini-key", query["key"])
	}
}

func TestModelsRequestOmitsEmptyCredentials(t *testing.T) {
	req := modelsRequest{Type: "models"}

	raw, err := json.Marshal(&req)
	if err != nil {
		t.Fatalf("marshal models request: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal models request: %v", err)
	}
	if _, exists := payload["headers"]; exists {
		t.Fatalf("headers should be omitted when empty, got %v", payload["headers"])
	}
	if _, exists := payload["query"]; exists {
		t.Fatalf("query should be omitted when empty, got %v", payload["query"])
	}
}

func TestQueryToLowerMap(t *testing.T) {
	got := queryToLowerMap(url.Values{
		"Key":   {"v1", "v2"},
		"Token": {"abc"},
	})
	if got["key"] != "v1, v2" {
		t.Fatalf("key = %q, want %q", got["key"], "v1, v2")
	}
	if got["token"] != "abc" {
		t.Fatalf("token = %q, want %q", got["token"], "abc")
	}

	if nilMap := queryToLowerMap(nil); nilMap != nil {
		t.Fatalf("queryToLowerMap(nil) = %v, want nil", nilMap)
	}
}

func TestClientSetLifecycleConfigAcceptsHomeAuthoritativeHeartbeat(t *testing.T) {
	client := New(config.HomeConfig{Enabled: true, Host: "127.0.0.1", Port: 6379})
	cfg := (config.CredentialConcurrencyConfig{}).WithDefaults()
	cfg.CPAHeartbeatTimeout = 20 * time.Second

	if errSet := client.SetLifecycleConfig(cfg); errSet != nil {
		t.Fatalf("SetLifecycleConfig() error = %v", errSet)
	}
	if got := client.LimiterConfig().CPAHeartbeatTimeout; got != cfg.CPAHeartbeatTimeout {
		t.Fatalf("LimiterConfig().CPAHeartbeatTimeout = %s, want %s", got, cfg.CPAHeartbeatTimeout)
	}
}

func TestConfigSubscriberUsesAppliedLifecycleRevisionAndRebuildsCommands(t *testing.T) {
	client := New(config.HomeConfig{Enabled: true, Host: "127.0.0.1", Port: 6379})
	client.mu.Lock()
	client.cmd = redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
	client.mu.Unlock()
	if errSet := client.SetLifecycleConfig(config.CredentialConcurrencyConfig{
		LifecycleConfigRevision: 9,
		CPAHeartbeatTimeout:     4 * time.Second,
		CPACancelBound:          5 * time.Second,
	}); errSet != nil {
		t.Fatalf("SetLifecycleConfig() error = %v", errSet)
	}
	args, timeout := client.subscriptionParameters()
	if !reflect.DeepEqual(args, []string{"config", "9"}) {
		t.Fatalf("subscribe args = %#v", args)
	}
	if timeout != 4*time.Second {
		t.Fatalf("receive timeout = %s", timeout)
	}
	client.promoteSubscription()
	client.mu.Lock()
	commandClient := client.cmd
	client.mu.Unlock()
	if commandClient != nil {
		t.Fatal("bootstrap command client was retained after subscription")
	}
}

func TestRunConfigSubscriberLifetimeReturnsAfterHeartbeatLoss(t *testing.T) {
	configPayload := "credential-concurrency:\n  lifecycle-config-revision: 1\n  cpa-heartbeat-timeout: 20ms\n"
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	commands := &redisCommandLog{}
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				reader := bufio.NewReader(conn)
				for {
					args, errRead := readRedisCommand(reader)
					if errRead != nil {
						return
					}
					commands.Append(args)
					switch {
					case len(args) >= 1 && strings.EqualFold(args[0], "HELLO"):
						if _, errWrite := io.WriteString(conn, "%6\r\n$6\r\nserver\r\n$5\r\nredis\r\n$5\r\nproto\r\n:3\r\n$2\r\nid\r\n:1\r\n$4\r\nmode\r\n$10\r\nstandalone\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$7\r\nmodules\r\n*0\r\n"); errWrite != nil {
							return
						}
					case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == redisKeyConfig:
						if _, errWrite := io.WriteString(conn, fmt.Sprintf("$%d\r\n%s\r\n", len(configPayload), configPayload)); errWrite != nil {
							return
						}
					case len(args) >= 2 && strings.EqualFold(args[0], "SUBSCRIBE") && args[1] == redisChannelConfig:
						if _, errWrite := io.WriteString(conn, "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n"); errWrite != nil {
							return
						}
					default:
						if _, errWrite := io.WriteString(conn, "+OK\r\n"); errWrite != nil {
							return
						}
					}
				}
			}()
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
		t.Fatalf("parse listener port: %v", errPort)
	}
	client := New(config.HomeConfig{Enabled: true, Host: host, Port: port, DisableClusterDiscovery: true})

	ready := make(chan struct{}, 1)
	errRun := client.RunConfigSubscriberLifetime(context.Background(), func(raw []byte) error {
		parsed, errParse := config.ParseConfigBytes(raw)
		if errParse != nil {
			return errParse
		}
		if errSet := client.SetLifecycleConfig(parsed.CredentialConcurrency); errSet != nil {
			return errSet
		}
		return nil
	}, func() { ready <- struct{}{} })
	if errRun == nil {
		t.Fatal("RunConfigSubscriberLifetime() error = nil after heartbeat loss")
	}
	select {
	case <-ready:
	default:
		t.Fatalf("RunConfigSubscriberLifetime() did not invoke onReady after subscription ACK: %v; commands=%#v", errRun, commands.All())
	}
	if client.HeartbeatOK() {
		t.Fatal("HeartbeatOK() = true after heartbeat loss")
	}
	client.mu.Lock()
	commandClient, subscriptionClient := client.cmd, client.sub
	client.mu.Unlock()
	if commandClient != nil || subscriptionClient != nil {
		t.Fatalf("clients retained after heartbeat loss: command=%v subscription=%v", commandClient != nil, subscriptionClient != nil)
	}
	if count := commands.CountCommandKey("GET", redisKeyConfig); count != 1 {
		t.Fatalf("GET config count = %d, want 1", count)
	}
	if count := commands.CountCommandKey("SUBSCRIBE", redisChannelConfig); count != 1 {
		t.Fatalf("SUBSCRIBE config count = %d, want 1", count)
	}
	if got := findRedisCommand(commands.All(), "SUBSCRIBE"); !reflect.DeepEqual(got, []string{"subscribe", "config", "1"}) {
		t.Fatalf("SUBSCRIBE wire command = %#v, want []string{\"subscribe\", \"config\", \"1\"}", got)
	}
}

func TestRunConfigSubscriberLifetimeRejectsInvalidSubscriptionACK(t *testing.T) {
	for name, ack := range map[string]string{
		"message":       "*3\r\n$7\r\nmessage\r\n$6\r\nconfig\r\n$2\r\n{}\r\n",
		"wrong-channel": "*3\r\n$9\r\nsubscribe\r\n$5\r\nother\r\n:1\r\n",
		"wrong-count":   "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:2\r\n",
	} {
		t.Run(name, func(t *testing.T) {
			client, commands := newRedisCommandTestClient(t, func(args []string) string {
				switch {
				case len(args) >= 1 && strings.EqualFold(args[0], "HELLO"):
					return "%6\r\n$6\r\nserver\r\n$5\r\nredis\r\n$5\r\nproto\r\n:3\r\n$2\r\nid\r\n:1\r\n$4\r\nmode\r\n$10\r\nstandalone\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$7\r\nmodules\r\n*0\r\n"
				case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == redisKeyConfig:
					return "$16\r\nhost: 127.0.0.1\r\n"
				case len(args) >= 2 && strings.EqualFold(args[0], "SUBSCRIBE") && args[1] == redisChannelConfig:
					return ack
				default:
					return "+OK\r\n"
				}
			})
			errRun := client.RunConfigSubscriberLifetime(context.Background(), func([]byte) error { return nil }, nil)
			if errRun == nil {
				t.Fatal("RunConfigSubscriberLifetime() error = nil, want invalid ACK rejection")
			}
			if command := findRedisCommand(commands.All(), "PING"); command != nil {
				t.Fatalf("PING command = %#v, want no command pool exposure before valid ACK", command)
			}
		})
	}
}

func TestReceiveSubscriptionACKsForMultipleChannels(t *testing.T) {
	firstACK := "*3\r\n$9\r\nsubscribe\r\n$5\r\nfirst\r\n:1\r\n"
	secondACK := "*3\r\n$9\r\nsubscribe\r\n$6\r\nsecond\r\n:2\r\n"
	tests := []struct {
		name     string
		response string
		wantErr  bool
	}{
		{name: "ordered final count", response: firstACK + secondACK},
		{name: "missing final ACK", response: firstACK, wantErr: true},
		{name: "wrong second channel", response: firstACK + "*3\r\n$9\r\nsubscribe\r\n$5\r\nother\r\n:2\r\n", wantErr: true},
		{name: "wrong second kind", response: firstACK + "*3\r\n$11\r\nunsubscribe\r\n$6\r\nsecond\r\n:2\r\n", wantErr: true},
		{name: "wrong second count", response: firstACK + "*3\r\n$9\r\nsubscribe\r\n$6\r\nsecond\r\n:1\r\n", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := newRedisCommandTestClient(t, func(args []string) string {
				switch {
				case len(args) >= 1 && strings.EqualFold(args[0], "HELLO"):
					return "%6\r\n$6\r\nserver\r\n$5\r\nredis\r\n$5\r\nproto\r\n:3\r\n$2\r\nid\r\n:1\r\n$4\r\nmode\r\n$10\r\nstandalone\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$7\r\nmodules\r\n*0\r\n"
				case len(args) == 3 && strings.EqualFold(args[0], "SUBSCRIBE") && args[1] == "first" && args[2] == "second":
					return tt.response
				default:
					return "-ERR unexpected command\r\n"
				}
			})
			pubsub := client.cmd.Subscribe(context.Background(), "first", "second")
			t.Cleanup(func() {
				if errClose := pubsub.Close(); errClose != nil {
					t.Errorf("close PubSub: %v", errClose)
				}
			})

			errACK := receiveSubscriptionACKs(context.Background(), pubsub, homeRedisTestOperationTimeout, []string{"first", "second"})
			if (errACK != nil) != tt.wantErr {
				t.Fatalf("receiveSubscriptionACKs() error = %v, wantErr %t", errACK, tt.wantErr)
			}
		})
	}
}

func TestRunConfigSubscriberLifetimeRejectsNonPositiveLifecycleDuration(t *testing.T) {
	configPayload := "credential-concurrency:\n" +
		"  lifecycle-config-revision: 1\n" +
		"  cpa-heartbeat-timeout: 0s\n" +
		"  cpa-cancel-bound: 5s\n" +
		"  reclaim-grace: 5s\n" +
		"  cleanup-interval: 5s\n"
	client, commands := newRedisCommandTestClient(t, func(args []string) string {
		switch {
		case len(args) >= 1 && strings.EqualFold(args[0], "HELLO"):
			return "%6\r\n$6\r\nserver\r\n$5\r\nredis\r\n$5\r\nproto\r\n:3\r\n$2\r\nid\r\n:1\r\n$4\r\nmode\r\n$10\r\nstandalone\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$7\r\nmodules\r\n*0\r\n"
		case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == redisKeyConfig:
			return fmt.Sprintf("$%d\r\n%s\r\n", len(configPayload), configPayload)
		case len(args) >= 2 && strings.EqualFold(args[0], "SUBSCRIBE") && args[1] == redisChannelConfig:
			return "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n"
		default:
			return "+OK\r\n"
		}
	})

	errRun := client.RunConfigSubscriberLifetime(context.Background(), func(raw []byte) error {
		parsed, errParse := config.ParseConfigBytes(raw)
		if errParse != nil {
			return errParse
		}
		return client.SetLifecycleConfig(parsed.CredentialConcurrency)
	}, nil)
	if errRun == nil {
		t.Fatal("RunConfigSubscriberLifetime() error = nil, want invalid lifecycle duration rejection")
	}
	if got := findRedisCommand(commands.All(), "SUBSCRIBE"); got != nil {
		t.Fatalf("SUBSCRIBE wire command = %#v, want no subscription after invalid GET config", got)
	}
}

func TestRunConfigSubscriberLifetimeRejectsExplicitInvalidLifecycleConfig(t *testing.T) {
	configPayload := "credential-concurrency:\n" +
		"  lifecycle-config-revision: 0\n" +
		"  cpa-heartbeat-timeout: 20ms\n" +
		"  cpa-cancel-bound: 5s\n" +
		"  reclaim-grace: 5s\n" +
		"  cleanup-interval: 5s\n"
	client, commands := newRedisCommandTestClient(t, func(args []string) string {
		switch {
		case len(args) >= 1 && strings.EqualFold(args[0], "HELLO"):
			return "%6\r\n$6\r\nserver\r\n$5\r\nredis\r\n$5\r\nproto\r\n:3\r\n$2\r\nid\r\n:1\r\n$4\r\nmode\r\n$10\r\nstandalone\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$7\r\nmodules\r\n*0\r\n"
		case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == redisKeyConfig:
			return fmt.Sprintf("$%d\r\n%s\r\n", len(configPayload), configPayload)
		case len(args) >= 2 && strings.EqualFold(args[0], "SUBSCRIBE") && args[1] == redisChannelConfig:
			return "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n"
		default:
			return "+OK\r\n"
		}
	})

	errRun := client.RunConfigSubscriberLifetime(context.Background(), func(raw []byte) error {
		parsed, errParse := config.ParseConfigBytes(raw)
		if errParse != nil {
			return errParse
		}
		return client.SetLifecycleConfig(parsed.CredentialConcurrency)
	}, nil)
	if errRun == nil {
		t.Fatal("RunConfigSubscriberLifetime() error = nil, want invalid lifecycle config rejection")
	}
	if got := findRedisCommand(commands.All(), "SUBSCRIBE"); got != nil {
		t.Fatalf("SUBSCRIBE wire command = %#v, want no subscription after invalid GET config", got)
	}
}

type blockingSubscriptionCloser struct {
	started chan struct{}
	release chan struct{}
}

func (c *blockingSubscriptionCloser) Close() error {
	close(c.started)
	<-c.release
	return nil
}

func TestEndConfigSubscriberLifetimeClearsHeartbeatBeforeCloseBlocks(t *testing.T) {
	client := New(config.HomeConfig{Enabled: true})
	client.heartbeatOK.Store(true)
	closer := &blockingSubscriptionCloser{started: make(chan struct{}), release: make(chan struct{})}

	done := make(chan error, 1)
	go func() {
		done <- client.endConfigSubscriberLifetimeWithSubscription(errors.New("heartbeat lost"), closer, "heartbeat loss")
	}()

	select {
	case <-closer.started:
	case <-time.After(time.Second):
		t.Fatal("subscription close did not start")
	}
	if client.heartbeatOK.Load() {
		close(closer.release)
		t.Fatal("HeartbeatOK() remained true while subscription close was blocked")
	}
	select {
	case errEnd := <-done:
		close(closer.release)
		t.Fatalf("endConfigSubscriberLifetimeWithSubscription() returned before subscription close unblocked: %v", errEnd)
	default:
	}
	close(closer.release)
	if errEnd := <-done; errEnd == nil {
		t.Fatal("endConfigSubscriberLifetimeWithSubscription() error = nil, want heartbeat loss")
	}
}

func TestRunConfigSubscriberLifetimeUsesLegacySubscribeWithoutLifecycleConfig(t *testing.T) {
	configPayload := "host: 127.0.0.1\n"
	client, commands := newRedisCommandTestClient(t, func(args []string) string {
		switch {
		case len(args) >= 1 && strings.EqualFold(args[0], "HELLO"):
			return "%6\r\n$6\r\nserver\r\n$5\r\nredis\r\n$5\r\nproto\r\n:3\r\n$2\r\nid\r\n:1\r\n$4\r\nmode\r\n$10\r\nstandalone\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$7\r\nmodules\r\n*0\r\n"
		case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == redisKeyConfig:
			return fmt.Sprintf("$%d\r\n%s\r\n", len(configPayload), configPayload)
		case len(args) >= 2 && strings.EqualFold(args[0], "SUBSCRIBE") && args[1] == redisChannelConfig:
			return "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n"
		default:
			return "+OK\r\n"
		}
	})

	errRun := client.RunConfigSubscriberLifetime(context.Background(), func(raw []byte) error {
		parsed, errParse := config.ParseConfigBytes(raw)
		if errParse != nil {
			return errParse
		}
		if errSet := client.SetLifecycleConfig(parsed.CredentialConcurrency); errSet != nil {
			return errSet
		}
		return nil
	}, nil)
	if errRun == nil {
		t.Fatal("RunConfigSubscriberLifetime() error = nil after heartbeat loss")
	}
	if got := findRedisCommand(commands.All(), "SUBSCRIBE"); !reflect.DeepEqual(got, []string{"subscribe", "config"}) {
		t.Fatalf("SUBSCRIBE wire command = %#v, want []string{\"subscribe\", \"config\"}", got)
	}
}

func TestRPopAuthLeavesCompleteServerErrorDeterministic(t *testing.T) {
	client, _ := newRedisCommandTestClient(t, func(args []string) string {
		switch {
		case len(args) >= 1 && strings.EqualFold(args[0], "HELLO"):
			return "%6\r\n$6\r\nserver\r\n$5\r\nredis\r\n$5\r\nproto\r\n:3\r\n$2\r\nid\r\n:1\r\n$4\r\nmode\r\n$10\r\nstandalone\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$7\r\nmodules\r\n*0\r\n"
		case len(args) >= 1 && strings.EqualFold(args[0], "RPOP"):
			return "-ERR dispatch denied\r\n"
		default:
			return "+OK\r\n"
		}
	})
	client.heartbeatOK.Store(true)

	_, errRPop := client.RPopAuth(context.Background(), "gpt-5.4", "", nil, 1)
	if errRPop == nil {
		t.Fatal("RPopAuth() error = nil, want server failure")
	}
	if IsAmbiguousDispatchError(errRPop) {
		t.Fatalf("RPopAuth() error = %v, want deterministic server error", errRPop)
	}
	if client.dispatchFenced.Load() || !client.heartbeatOK.Load() {
		t.Fatalf("client fence/heartbeat = %v/%v, want false/true", client.dispatchFenced.Load(), client.heartbeatOK.Load())
	}
}

type testRedisServerError string

func (e testRedisServerError) Error() string { return string(e) }
func (testRedisServerError) RedisError()     {}

func TestIssuedRPopAuthErrorClassification(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		ambiguous bool
	}{
		{name: "redis server error", err: testRedisServerError("ERR denied"), ambiguous: false},
		{name: "redis nil", err: redis.Nil, ambiguous: false},
		{name: "closed connection", err: redis.ErrClosed, ambiguous: true},
		{name: "pool timeout", err: redis.ErrPoolTimeout, ambiguous: true},
		{name: "dial interruption", err: &net.OpError{Op: "dial", Err: errors.New("connection refused")}, ambiguous: true},
		{name: "tls interruption", err: x509.UnknownAuthorityError{}, ambiguous: true},
		{name: "write interruption", err: &net.OpError{Op: "write", Err: io.ErrClosedPipe}, ambiguous: true},
		{name: "partial response", err: io.ErrUnexpectedEOF, ambiguous: true},
		{name: "unknown transport", err: errors.New("unknown transport state"), ambiguous: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAmbiguousIssuedRPopAuthError(tt.err); got != tt.ambiguous {
				t.Fatalf("isAmbiguousIssuedRPopAuthError(%v) = %v, want %v", tt.err, got, tt.ambiguous)
			}
		})
	}
}

func TestRPopAuthRejectsPreCanceledContextBeforeRequest(t *testing.T) {
	client, commands := newRedisCommandTestClient(t, func([]string) string { return "+OK\r\n" })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, errRPop := client.RPopAuth(ctx, "gpt-5.4", "", nil, 1)
	if !errors.Is(errRPop, context.Canceled) {
		t.Fatalf("RPopAuth() error = %v, want context.Canceled", errRPop)
	}
	if IsAmbiguousDispatchError(errRPop) {
		t.Fatalf("RPopAuth() error = %v, want deterministic pre-send cancellation", errRPop)
	}
	if commands.CountCommandKey("RPOP", "") != 0 {
		t.Fatalf("commands = %#v, want no RPOP", commands.All())
	}
}

func TestRPopAuthMarksRequestReadThenCloseAmbiguous(t *testing.T) {
	client, requestRead, release := newBlockingRPopTestClient(t)
	result := make(chan error, 1)
	go func() {
		_, errRPop := client.RPopAuth(context.Background(), "gpt-5.4", "", nil, 1)
		result <- errRPop
	}()
	select {
	case <-requestRead:
	case <-time.After(time.Second):
		t.Fatal("server did not read RPOP request")
	}
	close(release)
	if errRPop := <-result; !IsAmbiguousDispatchError(errRPop) {
		t.Fatalf("RPopAuth() error = %v, want ambiguous response interruption", errRPop)
	}
}

func TestRPopAuthLeavesHELLOSetupInterruptionDeterministic(t *testing.T) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("listen: %v", errListen)
	}
	commands := &redisCommandLog{}
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				args, errRead := readRedisCommand(bufio.NewReader(conn))
				if errRead == nil {
					commands.Append(args)
				}
			}()
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
		t.Fatalf("parse listener port: %v", errPort)
	}
	client := New(config.HomeConfig{Enabled: true, Host: host, Port: port, DisableClusterDiscovery: true})
	t.Cleanup(client.Close)

	_, errRPop := client.RPopAuth(context.Background(), "gpt-5.4", "", nil, 1)
	if errRPop == nil {
		t.Fatal("RPopAuth() error = nil, want setup interruption")
	}
	if IsAmbiguousDispatchError(errRPop) {
		t.Fatalf("RPopAuth() error = %v, want deterministic setup interruption", errRPop)
	}
	if client.dispatchFenced.Load() {
		t.Fatal("RPopAuth() fenced the client after setup interruption")
	}
	allCommands := commands.All()
	if len(allCommands) == 0 || len(allCommands[0]) == 0 || !strings.EqualFold(allCommands[0][0], "HELLO") {
		t.Fatalf("commands = %#v, want HELLO setup before interruption", allCommands)
	}
	for _, command := range allCommands {
		if len(command) > 0 && strings.EqualFold(command[0], "RPOP") {
			t.Fatalf("commands = %#v, want no RPOP after setup interruption", allCommands)
		}
	}
}

func TestTrackedRedisConnectionCloseRemovesContendedEntries(t *testing.T) {
	client := New(config.HomeConfig{Enabled: true})
	const connectionCount = 32
	connections := make([]*homeDispatchConn, 0, connectionCount)
	peers := make([]net.Conn, 0, connectionCount)
	for range connectionCount {
		local, peer := net.Pipe()
		connections = append(connections, &homeDispatchConn{Conn: local, client: client})
		peers = append(peers, peer)
	}
	t.Cleanup(func() {
		for _, peer := range peers {
			_ = peer.Close()
		}
	})

	client.mu.Lock()
	client.connections = make(map[*homeDispatchConn]struct{}, len(connections))
	for _, conn := range connections {
		client.connections[conn] = struct{}{}
	}
	started := make(chan struct{}, len(connections))
	closed := make(chan error, len(connections))
	for _, conn := range connections {
		go func(conn *homeDispatchConn) {
			started <- struct{}{}
			closed <- conn.Close()
		}(conn)
	}
	for range connections {
		<-started
	}
	time.Sleep(20 * time.Millisecond)
	client.mu.Unlock()
	for range connections {
		if errClose := <-closed; errClose != nil && !errors.Is(errClose, net.ErrClosed) {
			t.Fatalf("tracked connection close: %v", errClose)
		}
	}
	client.mu.Lock()
	remaining := len(client.connections)
	client.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("tracked connection count = %d, want 0 after contended close churn", remaining)
	}
}

func TestAbortAmbiguousDispatchClosesBlockedRPopWithoutWaitingForResponse(t *testing.T) {
	client, requestRead, release := newBlockingRPopTestClient(t)
	client.heartbeatOK.Store(true)
	result := make(chan error, 1)
	go func() {
		_, errRPop := client.RPopAuth(context.Background(), "gpt-5.4", "", nil, 1)
		result <- errRPop
	}()
	select {
	case <-requestRead:
	case <-time.After(time.Second):
		t.Fatal("server did not read RPOP request")
	}

	aborted := make(chan struct{})
	go func() {
		client.AbortAmbiguousDispatch()
		close(aborted)
	}()
	select {
	case <-aborted:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("AbortAmbiguousDispatch() waited for blocked RPOP response")
	}
	if client.heartbeatOK.Load() {
		close(release)
		t.Fatal("HeartbeatOK() remained true after abort")
	}
	client.mu.Lock()
	commandClient, subscriptionClient := client.cmd, client.sub
	client.mu.Unlock()
	if commandClient != nil || subscriptionClient != nil {
		close(release)
		t.Fatalf("clients retained after abort: command=%v subscription=%v", commandClient != nil, subscriptionClient != nil)
	}
	select {
	case errRPop := <-result:
		if errRPop == nil {
			close(release)
			t.Fatal("RPopAuth() error = nil after client abort")
		}
	case <-time.After(time.Second):
		close(release)
		t.Fatal("RPopAuth() remained blocked after abort closed its client")
	}
	close(release)
}

func TestRPopAuthLeavesPreSendFailureDeterministic(t *testing.T) {
	client := New(config.HomeConfig{Enabled: true, Host: "127.0.0.1", Port: 6379})

	_, errRPop := client.RPopAuth(context.Background(), "", "", nil, 1)
	if errRPop == nil {
		t.Fatal("RPopAuth() error = nil, want requested model validation failure")
	}
	if IsAmbiguousDispatchError(errRPop) {
		t.Fatalf("RPopAuth() error = %v, want deterministic pre-send failure", errRPop)
	}
}

func TestClientClosePermanentlyFencesDispatch(t *testing.T) {
	client := New(config.HomeConfig{Enabled: true, Host: "127.0.0.1", Port: 6379})
	client.mu.Lock()
	client.cmd = redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
	client.mu.Unlock()

	client.Close()
	if _, errClient := client.commandClient(); !errors.Is(errClient, ErrDispatchFenced) {
		t.Fatalf("commandClient() error = %v, want ErrDispatchFenced", errClient)
	}
	client.mu.Lock()
	commandClient := client.cmd
	client.mu.Unlock()
	if commandClient != nil {
		t.Fatal("commandClient() recreated a command pool after Close")
	}
}

func TestAbortAmbiguousDispatchFencesConcurrentRPop(t *testing.T) {
	client := New(config.HomeConfig{Enabled: true, Host: "127.0.0.1", Port: 6379})
	client.AbortAmbiguousDispatch()

	const attempts = 32
	errs := make(chan error, attempts)
	var workers sync.WaitGroup
	for range attempts {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, errRPop := client.RPopAuth(context.Background(), "gpt-5.4", "", nil, 1)
			errs <- errRPop
		}()
	}
	workers.Wait()
	close(errs)

	for errRPop := range errs {
		if !errors.Is(errRPop, ErrDispatchFenced) {
			t.Fatalf("RPopAuth() error = %v, want ErrDispatchFenced", errRPop)
		}
	}
	client.mu.Lock()
	commandClient := client.cmd
	client.mu.Unlock()
	if commandClient != nil {
		t.Fatal("RPopAuth() recreated a command pool after AbortAmbiguousDispatch")
	}
}

func TestRunConfigSubscriberLifetimeRebuildsFreshCommandPoolBeforeReady(t *testing.T) {
	configPayload := "host: 127.0.0.1\n"
	client, commands := newRedisCommandTestClient(t, func(args []string) string {
		switch {
		case len(args) >= 1 && strings.EqualFold(args[0], "HELLO"):
			return "%6\r\n$6\r\nserver\r\n$5\r\nredis\r\n$5\r\nproto\r\n:3\r\n$2\r\nid\r\n:1\r\n$4\r\nmode\r\n$10\r\nstandalone\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$7\r\nmodules\r\n*0\r\n"
		case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == redisKeyConfig:
			return fmt.Sprintf("$%d\r\n%s\r\n", len(configPayload), configPayload)
		case len(args) >= 2 && strings.EqualFold(args[0], "SUBSCRIBE") && args[1] == redisChannelConfig:
			return "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n"
		case len(args) >= 1 && strings.EqualFold(args[0], "PING"):
			return "+PONG\r\n"
		default:
			return "+OK\r\n"
		}
	})
	var bootstrap *redis.Client
	var freshCommandClient *redis.Client
	ready := make(chan struct{}, 1)
	errRun := client.RunConfigSubscriberLifetime(context.Background(), func([]byte) error {
		client.mu.Lock()
		bootstrap = client.cmd
		client.mu.Unlock()
		return nil
	}, func() {
		client.mu.Lock()
		freshCommandClient = client.cmd
		client.mu.Unlock()
		ready <- struct{}{}
	})
	if errRun == nil {
		t.Fatal("RunConfigSubscriberLifetime() error = nil after heartbeat loss")
	}
	select {
	case <-ready:
	default:
		t.Fatalf("RunConfigSubscriberLifetime() did not invoke onReady: %v", errRun)
	}
	if bootstrap == nil || freshCommandClient == nil || freshCommandClient == bootstrap {
		t.Fatalf("command pools bootstrap=%p fresh=%p, want distinct non-nil pools", bootstrap, freshCommandClient)
	}
	if got := findRedisCommand(commands.All(), "PING"); got == nil {
		t.Fatalf("commands = %#v, want fresh command PING before onReady", commands.All())
	}
}

func TestRunConfigSubscriberLifetimeDoesNotReadyWhenFreshCommandProbeFails(t *testing.T) {
	configPayload := "host: 127.0.0.1\n"
	client, _ := newRedisCommandTestClient(t, func(args []string) string {
		switch {
		case len(args) >= 1 && strings.EqualFold(args[0], "HELLO"):
			return "%6\r\n$6\r\nserver\r\n$5\r\nredis\r\n$5\r\nproto\r\n:3\r\n$2\r\nid\r\n:1\r\n$4\r\nmode\r\n$10\r\nstandalone\r\n$4\r\nrole\r\n$6\r\nmaster\r\n$7\r\nmodules\r\n*0\r\n"
		case len(args) >= 2 && strings.EqualFold(args[0], "GET") && args[1] == redisKeyConfig:
			return fmt.Sprintf("$%d\r\n%s\r\n", len(configPayload), configPayload)
		case len(args) >= 2 && strings.EqualFold(args[0], "SUBSCRIBE") && args[1] == redisChannelConfig:
			return "*3\r\n$9\r\nsubscribe\r\n$6\r\nconfig\r\n:1\r\n"
		case len(args) >= 1 && strings.EqualFold(args[0], "PING"):
			return "-ERR fresh command probe failed\r\n"
		default:
			return "+OK\r\n"
		}
	})
	ready := make(chan struct{}, 1)
	errRun := client.RunConfigSubscriberLifetime(context.Background(), func([]byte) error { return nil }, func() { ready <- struct{}{} })
	if errRun == nil {
		t.Fatal("RunConfigSubscriberLifetime() error = nil, want fresh command probe failure")
	}
	select {
	case <-ready:
		t.Fatalf("RunConfigSubscriberLifetime() invoked onReady after fresh command probe failure: %v", errRun)
	default:
	}
	client.mu.Lock()
	commandClient, subscriptionClient := client.cmd, client.sub
	client.mu.Unlock()
	if commandClient != nil || subscriptionClient != nil {
		t.Fatalf("clients retained after fresh command probe failure: command=%v subscription=%v", commandClient != nil, subscriptionClient != nil)
	}
}

func findRedisCommand(commands [][]string, commandName string) []string {
	for _, command := range commands {
		if len(command) > 0 && strings.EqualFold(command[0], commandName) {
			return command
		}
	}
	return nil
}
