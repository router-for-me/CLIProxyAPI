package main

/*
#include <stdint.h>
#include <stdlib.h>
typedef struct { void* ptr; size_t len; } cliproxy_buffer;
typedef int (*host_call_fn)(void*,const char*,const uint8_t*,size_t,cliproxy_buffer*);
typedef void (*free_fn)(void*,size_t);
typedef struct { uint32_t abi_version; void* host_ctx; host_call_fn call; free_fn free_buffer; } cliproxy_host_api;
typedef int (*call_fn)(char*,uint8_t*,size_t,cliproxy_buffer*);
typedef void (*shutdown_fn)(void);
typedef struct { uint32_t abi_version; call_fn call; free_fn free_buffer; shutdown_fn shutdown; } cliproxy_plugin_api;
extern int issue6653Call(char*,uint8_t*,size_t,cliproxy_buffer*);
extern void issue6653Free(void*,size_t);
extern void issue6653Shutdown(void);
*/
import "C"

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
	"unsafe"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

//export cliproxy_plugin_init
func cliproxy_plugin_init(_ *C.cliproxy_host_api, p *C.cliproxy_plugin_api) C.int {
	if p == nil {
		return 1
	}
	p.abi_version = 1
	p.call = C.call_fn(C.issue6653Call)
	p.free_buffer = C.free_fn(C.issue6653Free)
	p.shutdown = C.shutdown_fn(C.issue6653Shutdown)
	return 0
}

type qaTerminalError struct {
	code    string
	status  int
	retry   int
	message string
}

func (e qaTerminalError) Error() string {
	if e.message != "" {
		return e.message
	}
	return "renewal rejected"
}
func envelope(result any, err error) []byte {
	d := ojson.NewDoc([]byte(`{}`))
	if err != nil {
		_ = d.Set("ok", false)
		fields := map[string]any{"code": "qa_native_error", "message": err.Error(), "http_status": 503}
		if terminal, ok := err.(qaTerminalError); ok {
			fields["code"] = terminal.code
			if terminal.retry > 0 {
				fields["retry_after_seconds"] = terminal.retry
			}
			delete(fields, "http_status")
			if terminal.status != 0 {
				fields["http_status"] = terminal.status
			}
		}
		_ = d.Set("error", fields)
	} else {
		_ = d.Set("ok", true)
		_ = d.Set("result", result)
	}
	return d.Bytes()
}
func metadata(raw []byte, path string) map[string]any {
	m := map[string]any{}
	for _, key := range []string{"access_token", "refresh_token", "expires_at", "qa_upstream", "type"} {
		v := ojson.GetBytes(raw, path+key)
		if v.Exists() {
			m[key] = v.String()
		}
	}
	return m
}
func callUpstream(m map[string]any, path string, body []byte) ([]byte, error) {
	target, _ := m["qa_upstream"].(string)
	parsed, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(parsed.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("fixture requires loopback")
	}
	req, err := http.NewRequest(http.MethodPost, target+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+fmt.Sprint(m["access_token"]))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("qa loopback status %d: %s", resp.StatusCode, payload)
	}
	return payload, nil
}
func dispatch(method string, raw []byte) (any, error) {
	switch method {
	case "plugin.register":
		return map[string]any{"schema_version": 11, "metadata": map[string]any{"Name": "qa-native-6653", "Version": "1", "Author": "QA fixture", "GitHubRepository": "https://example.invalid/qa"}, "capabilities": map[string]any{"auth_provider": true, "executor": true, "quota_provider": true, "executor_input_formats": []string{"openai", "openai-image"}, "executor_output_formats": []string{"openai", "openai-image"}}}, nil
	case "auth.identifier", "quota.identifier", "executor.identifier":
		return map[string]any{"identifier": "qa-native-6653"}, nil
	case "auth.parse":
		stored, err := base64.StdEncoding.DecodeString(ojson.GetBytes(raw, "RawJSON").String())
		if err != nil {
			return nil, err
		}
		m := metadata(stored, "")
		var policy *pluginapi.AuthRefreshPolicy
		if p := ojson.GetBytes(stored, "qa_policy"); p.Exists() {
			policy = &pluginapi.AuthRefreshPolicy{Mode: p.Get("mode").String(), ExpiryMarginSeconds: p.Get("expiry_margin_seconds").Int()}
		}
		return pluginapi.AuthParseResponse{Handled: true, Auth: pluginapi.AuthData{ID: ojson.GetBytes(stored, "id").String(), Provider: "qa-native-6653", StorageJSON: stored, Metadata: m, RefreshPolicy: policy}}, nil
	case "auth.refresh":
		m := metadata(raw, "Metadata.")
		payload, err := callUpstream(m, "/refresh", nil)
		if err != nil {
			return nil, err
		}
		if code := ojson.GetBytes(payload, "qa_error_code").String(); code != "" {
			return nil, qaTerminalError{code: code, status: int(ojson.GetBytes(payload, "qa_error_status").Int()), retry: int(ojson.GetBytes(payload, "qa_retry_after").Int()), message: ojson.GetBytes(payload, "qa_error_message").String()}
		}
		if code := ojson.GetBytes(payload, "qa_terminal_code").String(); code != "" {
			return nil, qaTerminalError{code: code, status: int(ojson.GetBytes(payload, "qa_terminal_status").Int())}
		}
		provider := "qa-native-6653"
		if responseProvider := ojson.GetBytes(payload, "qa_response_provider").String(); responseProvider != "" {
			provider = responseProvider
		}
		id := ojson.GetBytes(raw, "AuthID").String()
		if responseID := ojson.GetBytes(payload, "qa_response_id").String(); responseID != "" {
			id = responseID
		}
		m["access_token"] = ojson.GetBytes(payload, "access_token").String()
		m["refresh_token"] = ojson.GetBytes(payload, "refresh_token").String()
		m["expires_at"] = time.Now().Add(2 * time.Hour).Format(time.RFC3339Nano)
		d := ojson.NewDoc([]byte(`{}`))
		_ = d.Set("id", id)
		for key, value := range m {
			_ = d.Set(key, value)
		}
		return pluginapi.AuthRefreshResponse{Auth: pluginapi.AuthData{Provider: provider, ID: id, Metadata: m, StorageJSON: d.Bytes()}}, nil
	case "executor.http_request":
		payload, err := callUpstream(metadata(raw, "Metadata."), "/direct", nil)
		if err != nil {
			return nil, err
		}
		return pluginapi.ExecutorHTTPResponse{StatusCode: 200, Body: payload}, nil
	case "executor.execute", "executor.execute_stream", "executor.count_tokens":
		m := metadata(raw, "AuthMetadata.")
		path := "/chat"
		if method == "executor.execute_stream" {
			path = "/sse"
		}
		if method == "executor.count_tokens" {
			path = "/count"
		}
		if ojson.GetBytes(raw, "Metadata.request_path").String() == "/v1/images/generations" || ojson.GetBytes(raw, "Format").String() == "openai-image" {
			path = "/image"
		}
		payload, err := callUpstream(m, path, nil)
		if err != nil {
			return nil, err
		}
		if method == "executor.execute_stream" {
			return map[string]any{"chunks": []pluginapi.ExecutorStreamChunk{{Payload: payload}}}, nil
		}
		return pluginapi.ExecutorResponse{Payload: payload}, nil
	case "quota.fetch", "quota.reset":
		m := metadata(raw, "metadata.")
		_, err := callUpstream(m, "/quota", nil)
		if err != nil {
			return nil, err
		}
		if method == "quota.reset" {
			return pluginapi.QuotaResetResponse{}, nil
		}
		return pluginapi.QuotaFetchResponse{}, nil
	default:
		return map[string]any{}, nil
	}
}

//export issue6653Call
func issue6653Call(method *C.char, raw *C.uint8_t, n C.size_t, out *C.cliproxy_buffer) C.int {
	if out == nil {
		return 1
	}
	request := C.GoBytes(unsafe.Pointer(raw), C.int(n))
	result, err := dispatch(C.GoString(method), request)
	payload := envelope(result, err)
	out.ptr = C.CBytes(payload)
	out.len = C.size_t(len(payload))
	return 0
}

//export issue6653Free
func issue6653Free(p unsafe.Pointer, _ C.size_t) { C.free(p) }

//export issue6653Shutdown
func issue6653Shutdown() {}
func main()              {}
