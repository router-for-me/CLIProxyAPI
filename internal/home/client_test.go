package home

import (
	"bufio"
	"context"
	"crypto/tls"
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
