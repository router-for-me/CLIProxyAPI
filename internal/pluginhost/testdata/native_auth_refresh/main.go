package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct { void* ptr; size_t len; } cliproxy_buffer;
typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);
typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;
typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);
typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;
extern int nativeAuthRefreshCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void nativeAuthRefreshFree(void*, size_t);
extern void nativeAuthRefreshShutdown(void);
*/
import "C"

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"unsafe"
)

const provider = "native-auth-refresh"
const marker = "native-auth-refresh-c-shared-schema9-v1"

type credential struct {
	Type              string `json:"type"`
	ID                string `json:"id"`
	AccessToken       string `json:"access_token"`
	RefreshToken      string `json:"refresh_token"`
	Expired           string `json:"expired"`
	BaseURL           string `json:"base_url"`
	RefreshURL        string `json:"refresh_url"`
	RefreshMode       string `json:"refresh_mode,omitempty"`
	TestRefreshPolicy string `json:"test_refresh_policy,omitempty"`
}

type authData struct {
	Provider      string
	ID            string
	FileName      string
	StorageJSON   []byte
	Metadata      map[string]any
	Attributes    map[string]string
	RefreshPolicy string `json:"RefreshPolicy,omitempty"`
}

type refreshRequest struct {
	AuthID       string
	AuthProvider string
	StorageJSON  []byte
	Metadata     map[string]any
	Attributes   map[string]string
}

type rpcFailure struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	HTTPStatus int    `json:"http_status"`
}

type quotaRequest struct {
	AuthID      string            `json:"auth_id"`
	Provider    string            `json:"provider"`
	StorageJSON []byte            `json:"storage_json"`
	Metadata    map[string]any    `json:"metadata"`
	Attributes  map[string]string `json:"attributes"`
}

var parses atomic.Int64
var refreshes atomic.Int64
var consistencyErrors atomic.Int64
var shutdowns atomic.Int64

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if host == nil || plugin == nil || host.abi_version != 1 {
		return 1
	}
	plugin.abi_version = 1
	parses.Store(0)
	refreshes.Store(0)
	consistencyErrors.Store(0)
	shutdowns.Store(0)
	plugin.call = C.cliproxy_plugin_call_fn(C.nativeAuthRefreshCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.nativeAuthRefreshFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.nativeAuthRefreshShutdown)
	return 0
}

//export nativeAuthRefreshCall
func nativeAuthRefreshCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if method == nil || response == nil {
		return 1
	}
	response.ptr = nil
	response.len = 0
	var raw []byte
	if request != nil && requestLen > 0 {
		raw = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	body := dispatch(C.GoString(method), raw)
	response.ptr = C.CBytes(body)
	response.len = C.size_t(len(body))
	if response.ptr == nil {
		return 1
	}
	return 0
}

//export nativeAuthRefreshFree
func nativeAuthRefreshFree(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export nativeAuthRefreshShutdown
func nativeAuthRefreshShutdown() {
	shutdowns.Add(1)
}

func dispatch(method string, raw []byte) []byte {
	switch method {
	case "test.marker":
		return []byte(marker)
	case "test.counters":
		return marshal(map[string]any{"marker": marker, "parse": parses.Load(), "refresh": refreshes.Load(), "consistency_errors": consistencyErrors.Load(), "shutdown": shutdowns.Load()})
	case "plugin.register", "plugin.reconfigure":
		var host struct {
			SchemaVersion uint32 `json:"schema_version"`
		}
		if json.Unmarshal(raw, &host) != nil || host.SchemaVersion < 9 {
			return failure("fixture_schema", "on-demand fixture requires host schema 9", 400)
		}
		return success(map[string]any{
			"schema_version": 9,
			"metadata":       map[string]any{"Name": provider, "Version": "1.0.0", "Author": "synthetic-native-fixture", "GitHubRepository": "https://github.com/router-for-me/CLIProxyAPI"},
			"capabilities":   map[string]any{"auth_provider": true, "quota_provider": true},
		})
	case "plugin.quiesce":
		return success(map[string]any{})
	case "auth.identifier":
		return success(map[string]any{"identifier": provider})
	case "quota.identifier":
		return success(map[string]any{"identifier": provider})
	case "quota.describe":
		return success(map[string]any{"supported_providers": []string{provider}, "supports_reset": true})
	case "quota.fetch", "quota.reset":
		var req quotaRequest
		if json.Unmarshal(raw, &req) != nil {
			return failure("fixture_quota", "invalid quota context", 400)
		}
		var c credential
		if json.Unmarshal(req.StorageJSON, &c) != nil || validate(c) != nil {
			return failure("fixture_quota", "invalid quota storage", 400)
		}
		if req.AuthID != c.ID || req.Provider != provider || req.Metadata["access_token"] != c.AccessToken || req.Metadata["refresh_token"] != c.RefreshToken || req.Attributes["api_key"] != c.AccessToken {
			consistencyErrors.Add(1)
			return failure("fixture_quota", "quota tuple is incoherent", 400)
		}
		op := "quota"
		if method == "quota.reset" {
			op = "quota/reset"
		}
		httpReq, err := http.NewRequest(http.MethodPost, c.BaseURL+"/"+op, bytes.NewReader([]byte(`{}`)))
		if err != nil {
			return failure("fixture_quota", err.Error(), 400)
		}
		httpReq.Header.Set("Authorization", "Bearer "+c.AccessToken)
		transport := &http.Transport{Proxy: nil}
		defer transport.CloseIdleConnections()
		resp, err := (&http.Client{Transport: transport}).Do(httpReq)
		if err != nil {
			return failure("fixture_quota", err.Error(), 502)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil || resp.StatusCode != 200 {
			return failure("fixture_quota", string(body), resp.StatusCode)
		}
		if method == "quota.reset" {
			return success(map[string]any{"success": true})
		}
		return success(map[string]any{"subscription": map[string]any{"plan": "synthetic"}})
	case "auth.parse":
		parses.Add(1)
		var req struct {
			Provider string
			FileName string
			RawJSON  []byte
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			return failure("fixture_parse", err.Error(), 400)
		}
		var c credential
		if err := json.Unmarshal(req.RawJSON, &c); err != nil {
			return failure("fixture_parse", err.Error(), 400)
		}
		if req.Provider != provider || c.Type != provider {
			return success(map[string]any{"Handled": false})
		}
		if err := validate(c); err != nil {
			return failure("fixture_parse", err.Error(), 400)
		}
		return success(map[string]any{"Handled": true, "Auth": makeAuth(c, req.FileName)})
	case "auth.refresh":
		refreshes.Add(1)
		var req refreshRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			return failure("fixture_refresh", err.Error(), 400)
		}
		var c credential
		if err := json.Unmarshal(req.StorageJSON, &c); err != nil {
			return failure("fixture_refresh", err.Error(), 400)
		}
		if err := validate(c); err != nil {
			return failure("fixture_refresh", err.Error(), 400)
		}
		if req.AuthID != c.ID || req.AuthProvider != provider || req.Metadata["access_token"] != c.AccessToken || req.Metadata["refresh_token"] != c.RefreshToken || req.Metadata["expired"] != c.Expired || req.Attributes["api_key"] != c.AccessToken {
			consistencyErrors.Add(1)
			return failure("fixture_incoherent", "synthetic token views differ at native refresh boundary", 400)
		}
		payload := marshal(map[string]any{"refresh_token": c.RefreshToken, "refresh_mode": c.RefreshMode})
		httpReq, err := http.NewRequest(http.MethodPost, c.RefreshURL, bytes.NewReader(payload))
		if err != nil {
			return failure("fixture_refresh", err.Error(), 400)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		transport := &http.Transport{Proxy: nil}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("fixture redirects are forbidden") }}
		resp, err := client.Do(httpReq)
		if err != nil {
			return failure("fixture_refresh", err.Error(), 502)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return failure("fixture_refresh", err.Error(), 502)
		}
		if resp.StatusCode != http.StatusOK {
			return failure("fixture_upstream_refresh", string(body), resp.StatusCode)
		}
		var rotated struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			Expired      string `json:"expired"`
		}
		if err := json.Unmarshal(body, &rotated); err != nil {
			return failure("fixture_refresh", err.Error(), 502)
		}
		c.AccessToken, c.RefreshToken, c.Expired = rotated.AccessToken, rotated.RefreshToken, rotated.Expired
		if err := validate(c); err != nil {
			return failure("fixture_refresh", err.Error(), 502)
		}
		return success(map[string]any{"Auth": makeAuth(c, "")})
	default:
		return failure("fixture_unsupported", "unsupported synthetic fixture method "+method, 400)
	}
}

func validate(c credential) error {
	if c.TestRefreshPolicy != "" && c.TestRefreshPolicy != "on-demand" {
		return fmt.Errorf("unsupported synthetic refresh policy")
	}
	if c.Type != provider || c.ID == "" || c.Expired == "" || !strings.HasPrefix(c.AccessToken, "synthetic-") || !strings.HasPrefix(c.RefreshToken, "synthetic-") {
		return fmt.Errorf("only complete synthetic credentials are permitted")
	}
	for _, value := range []string{c.BaseURL, c.RefreshURL} {
		u, err := url.Parse(value)
		if err != nil || u.Scheme != "http" || u.User != nil {
			return fmt.Errorf("fixture requires an HTTP loopback URL")
		}
		ip := net.ParseIP(u.Hostname())
		if ip == nil || !ip.IsLoopback() {
			return fmt.Errorf("fixture requires a literal loopback address")
		}
	}
	return nil
}

func makeAuth(c credential, filename string) authData {
	return authData{
		Provider: provider, ID: c.ID, FileName: filename, StorageJSON: marshal(c),
		RefreshPolicy: c.TestRefreshPolicy,
		Metadata:      map[string]any{"type": c.Type, "id": c.ID, "access_token": c.AccessToken, "refresh_token": c.RefreshToken, "expired": c.Expired, "base_url": c.BaseURL, "refresh_url": c.RefreshURL, "refresh_mode": c.RefreshMode, "auth_kind": "oauth"},
		Attributes:    map[string]string{"base_url": c.BaseURL, "api_key": c.AccessToken, "auth_kind": "oauth"},
	}
}

func marshal(value any) []byte {
	out, err := json.Marshal(value)
	if err != nil {
		return []byte(`{"ok":false,"error":{"code":"fixture_json","message":"fixture JSON encoding failed","http_status":500}}`)
	}
	return out
}

func success(value any) []byte {
	return marshal(map[string]any{"ok": true, "result": value})
}

func failure(code, message string, status int) []byte {
	return marshal(map[string]any{"ok": false, "error": rpcFailure{Code: code, Message: message, HTTPStatus: status}})
}

func main() {}
