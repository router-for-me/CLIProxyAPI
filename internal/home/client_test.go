package home

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
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
	client.cmd = redis.NewClient(&redis.Options{
		Addr:                  listener.Addr().String(),
		Protocol:              2,
		DisableIdentity:       true,
		MaxRetries:            -1,
		ContextTimeoutEnabled: true,
	})
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
