#!/usr/bin/env python3
from __future__ import annotations

import json
from pathlib import Path
from typing import NamedTuple


ROOT = Path(__file__).resolve().parents[1]
ABI_VERSION = 1
SCHEMA_VERSION = 1


class Capability(NamedTuple):
    slug: str
    title: str
    capability_json: str
    methods: tuple[str, ...]
    description_cn: str
    description_en: str


CAPABILITIES = (
    Capability("model", "Model", '"model_provider":true', ("model.static", "model.for_auth"), "模型能力示例，只返回静态模型和按认证发现模型。", "Model capability example with static and auth-bound models."),
    Capability("auth", "Auth", '"auth_provider":true', ("auth.identifier", "auth.parse", "auth.login.start", "auth.login.poll", "auth.refresh"), "认证能力示例，演示解析、登录、轮询和刷新。", "Auth capability example with parse, login, poll, and refresh."),
    Capability("frontend-auth", "Frontend Auth", '"frontend_auth_provider":true', ("frontend_auth.identifier", "frontend_auth.authenticate"), "前端认证能力示例，演示代理入口前认证。", "Frontend auth capability example."),
    Capability("executor", "Executor", '"executor":true,"executor_model_scope":"both","executor_input_formats":["chat-completions"],"executor_output_formats":["chat-completions"]', ("executor.identifier", "executor.execute", "executor.execute_stream", "executor.count_tokens", "executor.http_request"), "执行器能力示例，演示普通执行、流式执行、计数和 HTTP 请求。", "Executor capability example."),
    Capability("protocol-format", "Protocol Format", '"executor":true,"executor_model_scope":"both","executor_input_formats":["chat-completions"],"executor_output_formats":["responses"]', ("executor.identifier", "executor.execute"), "协议格式适配示例，用最小执行器承载格式声明。", "Protocol format example carried by a minimal executor."),
    Capability("request-translator", "Request Translator", '"request_translator":true', ("request.translate",), "请求转换能力示例。", "Request translator capability example."),
    Capability("request-normalizer", "Request Normalizer", '"request_normalizer":true', ("request.normalize",), "请求规整能力示例。", "Request normalizer capability example."),
    Capability("response-translator", "Response Translator", '"response_translator":true', ("response.translate",), "响应转换能力示例。", "Response translator capability example."),
    Capability("response-normalizer", "Response Normalizer", '"response_before_translator":true,"response_after_translator":true', ("response.normalize_before", "response.normalize_after"), "响应规整能力示例。", "Response normalizer capability example."),
    Capability("thinking", "Thinking", '"thinking_applier":true', ("thinking.identifier", "thinking.apply"), "Thinking 能力示例。", "Thinking applier capability example."),
    Capability("usage", "Usage", '"usage_plugin":true', ("usage.handle",), "Usage 能力示例。", "Usage observer capability example."),
    Capability("cli", "CLI", '"command_line_plugin":true', ("command_line.register", "command_line.execute"), "命令行扩展能力示例。", "Command-line capability example."),
    Capability("management-api", "Management API", '"management_api":true', ("management.register", "management.handle"), "Management API 扩展能力示例。", "Management API capability example."),
    Capability("host-callback", "Host Callback", '"management_api":true', ("management.register", "management.handle"), "Host callback 示例，用最小 Management API 入口触发宿主 HTTP 和日志回调。", "Host callback example carried by a minimal Management API route."),
)


def plugin_id(cap: Capability, lang: str) -> str:
    return f"example-{cap.slug}-{lang}"


def write(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content, encoding="utf-8")


def json_string(value: str) -> str:
    return json.dumps(value)


def compact_json(value: object) -> str:
    return json.dumps(value, separators=(",", ":"))


def c_ident(slug: str) -> str:
    return slug.replace("-", "_")


def registration_result(cap: Capability, lang: str) -> str:
    pid = plugin_id(cap, lang)
    return (
        "{"
        f'"schema_version":{SCHEMA_VERSION},'
        '"metadata":{'
        f'"Name":{json.dumps(pid)},'
        '"Version":"0.1.0",'
        '"Author":"router-for-me",'
        '"GitHubRepository":"https://github.com/router-for-me/CLIProxyAPI",'
        f'"Logo":"https://example.invalid/{pid}.png",'
        '"ConfigFields":[]'
        "},"
        f'"capabilities":{{{cap.capability_json}}}'
        "}"
    )


def model_result(cap: Capability, lang: str) -> str:
    pid = plugin_id(cap, lang)
    return (
        "{"
        f'"Provider":{json.dumps(pid)},'
        '"Models":[{'
        f'"ID":{json.dumps(pid + "-model")},'
        '"Object":"model",'
        f'"OwnedBy":{json.dumps(pid)},'
        f'"DisplayName":{json.dumps(cap.title + " Example Model")},'
        '"SupportedGenerationMethods":["chat"],'
        '"ContextLength":8192,'
        '"MaxCompletionTokens":1024,'
        '"UserDefined":true'
        "}]"
        "}"
    )


def auth_data_result(cap: Capability, lang: str) -> str:
    pid = plugin_id(cap, lang)
    return (
        "{"
        f'"Provider":{json.dumps(pid)},'
        f'"ID":{json.dumps(pid)},'
        f'"FileName":{json.dumps(pid + ".json")},'
        f'"Label":{json.dumps(cap.title + " Example")},'
        f'"StorageJSON":{json.dumps(base64_json({"type": pid, "token": "example-token"}))},'
        f'"Metadata":{{"type":{json.dumps(pid)}}}'
        "}"
    )


def base64_json(value: object) -> str:
    import base64

    raw = json.dumps(value, separators=(",", ":")).encode()
    return base64.b64encode(raw).decode()


def result_for_method(cap: Capability, lang: str, method: str) -> str:
    pid = plugin_id(cap, lang)
    if method in ("plugin.register", "plugin.reconfigure"):
        return registration_result(cap, lang)
    if method == "model.static" or method == "model.for_auth":
        return model_result(cap, lang)
    if method.endswith(".identifier"):
        return f'{{"identifier":{json.dumps(pid)}}}'
    if method == "auth.parse":
        return f'{{"Handled":true,"Auth":{auth_data_result(cap, lang)}}}'
    if method == "auth.login.start":
        return f'{{"Provider":{json.dumps(pid)},"URL":"https://example.invalid/login","State":"example-state","ExpiresAt":"2030-01-01T00:00:00Z"}}'
    if method == "auth.login.poll":
        return f'{{"Status":"success","Message":"example login complete","Auth":{auth_data_result(cap, lang)}}}'
    if method == "auth.refresh":
        return f'{{"Auth":{auth_data_result(cap, lang)},"NextRefreshAfter":"2030-01-01T00:00:00Z"}}'
    if method == "frontend_auth.authenticate":
        return compact_json({"Authenticated": True, "Principal": pid, "Metadata": {"provider": pid}})
    if method == "executor.execute":
        return compact_json({"Payload": base64_json({"id": pid, "object": "chat.completion"}), "Headers": {"content-type": ["application/json"]}})
    if method == "executor.execute_stream":
        return compact_json({"headers": {"content-type": ["text/event-stream"]}, "chunks": [{"Payload": base64_json("data: " + pid + "\n\n")}]})
    if method == "executor.count_tokens":
        return compact_json({"Payload": base64_json({"total_tokens": 0})})
    if method == "executor.http_request":
        return compact_json({"StatusCode": 200, "Headers": {"content-type": ["application/json"]}, "Body": base64_json({"plugin": pid})})
    if method == "request.translate":
        return compact_json({"Body": base64_json({"translated_by": pid})})
    if method == "request.normalize":
        return compact_json({"Body": base64_json({"normalized_by": pid})})
    if method == "response.translate":
        return compact_json({"Body": base64_json({"response_translated_by": pid})})
    if method == "response.normalize_before":
        return compact_json({"Body": base64_json({"response_normalized_before_by": pid})})
    if method == "response.normalize_after":
        return compact_json({"Body": base64_json({"response_normalized_after_by": pid})})
    if method == "thinking.apply":
        return compact_json({"Body": base64_json({"thinking_applied_by": pid})})
    if method == "usage.handle":
        return "{}"
    if method == "command_line.register":
        return f'{{"Flags":[{{"Name":{json.dumps(pid + "-command")},"Usage":"Run the example plugin command","Type":"bool"}}]}}'
    if method == "command_line.execute":
        return f'{{"Stdout":{json.dumps(base64_json(pid + " command executed\\n"))},"ExitCode":0}}'
    if method == "management.register":
        return f'{{"routes":[{{"Method":"GET","Path":"/plugins/{pid}/status","Menu":{json.dumps(cap.title)},"Description":{json.dumps(cap.description_en)}}}]}}'
    if method == "management.handle":
        return compact_json({"StatusCode": 200, "Headers": {"content-type": ["application/json"]}, "Body": base64_json({"plugin": pid})})
    raise ValueError(f"unsupported method {method}")


def envelope(result: str) -> str:
    return f'{{"ok":true,"result":{result}}}'


def error_envelope(code: str, message: str) -> str:
    return json.dumps({"ok": False, "error": {"code": code, "message": message}}, separators=(",", ":"))


def methods_for(cap: Capability) -> tuple[str, ...]:
    return ("plugin.register", "plugin.reconfigure", *cap.methods)


def generate_go(cap: Capability) -> None:
    slug = cap.slug
    pid = plugin_id(cap, "go")
    method_cases = []
    for method in methods_for(cap):
        host_callback_call = ""
        if slug == "host-callback" and method == "management.handle":
            host_callback_call = f"""\t\tcallHost("host.log", []byte(`{{"level":"info","message":"{pid} host callback log","fields":{{"plugin":"{pid}"}}}}`))
\t\tcallHost("host.http.do", []byte(`{{"method":"GET","url":"https://example.com","headers":{{"user-agent":["{pid}"]}}}}`))
"""
        method_cases.append(f'\tcase "{method}":\n{host_callback_call}\t\treturn okEnvelopeJSON({json.dumps(result_for_method(cap, "go", method))})')
    go_mod = f"""module github.com/router-for-me/CLIProxyAPI/v7/examples/plugin/{slug}/go

go 1.26
"""
    go_main = f"""package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {{
\tvoid* ptr;
\tsize_t len;
}} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {{
\tuint32_t abi_version;
\tvoid* host_ctx;
\tcliproxy_host_call_fn call;
\tcliproxy_host_free_fn free_buffer;
}} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {{
\tuint32_t abi_version;
\tcliproxy_plugin_call_fn call;
\tcliproxy_plugin_free_fn free_buffer;
\tcliproxy_plugin_shutdown_fn shutdown;
}} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {{
\tstored_host = host;
}}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {{
\tif (stored_host == NULL || stored_host->call == NULL) {{
\t\treturn 1;
\t}}
\treturn stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}}

static void free_host_buffer(void* ptr, size_t len) {{
\tif (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {{
\t\tstored_host->free_buffer(ptr, len);
\t}}
}}
*/
import "C"

import (
\t"encoding/json"
\t"net/http"
\t"time"
\t"unsafe"
)

const abiVersion uint32 = {ABI_VERSION}

type envelope struct {{
\tOK     bool            `json:"ok"`
\tResult json.RawMessage `json:"result,omitempty"`
\tError  *envelopeError  `json:"error,omitempty"`
}}

type envelopeError struct {{
\tCode    string `json:"code"`
\tMessage string `json:"message"`
}}

func main() {{}}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {{
\tif plugin == nil {{
\t\treturn 1
\t}}
\tC.store_host_api(host)
\tplugin.abi_version = C.uint32_t(abiVersion)
\tplugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
\tplugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
\tplugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
\treturn 0
}}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {{
\tif response != nil {{
\t\tresponse.ptr = nil
\t\tresponse.len = 0
\t}}
\tif method == nil {{
\t\twriteResponse(response, errorEnvelope("invalid_method", "method is required"))
\t\treturn 1
\t}}
\traw, errHandle := handleMethod(C.GoString(method))
\tif errHandle != nil {{
\t\twriteResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
\t\treturn 1
\t}}
\twriteResponse(response, raw)
\t_ = request
\t_ = requestLen
\treturn 0
}}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, len C.size_t) {{
\tif ptr != nil {{
\t\tC.free(ptr)
\t}}
\t_ = len
}}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {{}}

func handleMethod(method string) ([]byte, error) {{
\t_ = http.StatusOK
\t_ = time.Second
\tswitch method {{
{chr(10).join(method_cases)}
\tdefault:
\t\treturn errorEnvelope("unknown_method", "unknown method: "+method), nil
\t}}
}}

func okEnvelopeJSON(result string) ([]byte, error) {{
\treturn json.Marshal(envelope{{OK: true, Result: json.RawMessage(result)}})
}}

func errorEnvelope(code, message string) []byte {{
\traw, _ := json.Marshal(envelope{{OK: false, Error: &envelopeError{{Code: code, Message: message}}}})
\treturn raw
}}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {{
\tif response == nil || len(raw) == 0 {{
\t\treturn
\t}}
\tptr := C.CBytes(raw)
\tif ptr == nil {{
\t\treturn
\t}}
\tresponse.ptr = ptr
\tresponse.len = C.size_t(len(raw))
}}

func callHost(method string, payload []byte) {{
\tcMethod := C.CString(method)
\tdefer C.free(unsafe.Pointer(cMethod))
\tvar response C.cliproxy_buffer
\tvar req *C.uint8_t
\tif len(payload) > 0 {{
\t\treq = (*C.uint8_t)(C.CBytes(payload))
\t\tdefer C.free(unsafe.Pointer(req))
\t}}
\tif C.call_host_api(cMethod, req, C.size_t(len(payload)), &response) == 0 && response.ptr != nil {{
\t\tC.free_host_buffer(response.ptr, response.len)
\t}}
}}
"""
    write(ROOT / slug / "go" / "go.mod", go_mod)
    write(ROOT / slug / "go" / "main.go", go_main)


def c_string(value: str) -> str:
    return json.dumps(value)


def generate_c(cap: Capability) -> None:
    slug = cap.slug
    ident = c_ident(slug)
    pid = plugin_id(cap, "c")
    cases = []
    for method in methods_for(cap):
        result = envelope(result_for_method(cap, "c", method))
        host_call = ""
        if slug == "host-callback" and method == "management.handle":
            host_call = f"""
\t\tcall_host("host.log", "{{\\\"level\\\":\\\"info\\\",\\\"message\\\":\\\"{pid} host callback log\\\",\\\"fields\\\":{{\\\"plugin\\\":\\\"{pid}\\\"}}}}");
\t\tcall_host("host.http.do", "{{\\\"method\\\":\\\"GET\\\",\\\"url\\\":\\\"https://example.com\\\",\\\"headers\\\":{{\\\"user-agent\\\":[\\\"{pid}\\\"]}}}}");
"""
        cases.append(f"""\tif (strcmp(method, {c_string(method)}) == 0) {{{host_call}
\t\twrite_response(response, {c_string(result)});
\t\treturn 0;
\t}}""")
    cmake = f"""cmake_minimum_required(VERSION 3.16)
project(cliproxy_{ident}_c C)

add_library(cliproxy_{ident}_c SHARED src/plugin.c)
set_target_properties(cliproxy_{ident}_c PROPERTIES
    OUTPUT_NAME "{slug}-c"
    PREFIX ""
)
"""
    source = f"""#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#if defined(_WIN32)
#define CLIPROXY_EXPORT __declspec(dllexport)
#else
#define CLIPROXY_EXPORT __attribute__((visibility("default")))
#endif

#define ABI_VERSION {ABI_VERSION}

typedef struct {{
\tvoid* ptr;
\tsize_t len;
}} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {{
\tuint32_t abi_version;
\tvoid* host_ctx;
\tcliproxy_host_call_fn call;
\tcliproxy_host_free_fn free_buffer;
}} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {{
\tuint32_t abi_version;
\tcliproxy_plugin_call_fn call;
\tcliproxy_plugin_free_fn free_buffer;
\tcliproxy_plugin_shutdown_fn shutdown;
}} cliproxy_plugin_api;

static const cliproxy_host_api* stored_host = NULL;

static void write_response(cliproxy_buffer* response, const char* text) {{
\tif (response == NULL || text == NULL) {{
\t\treturn;
\t}}
\tsize_t len = strlen(text);
\tvoid* ptr = malloc(len);
\tif (ptr == NULL) {{
\t\tresponse->ptr = NULL;
\t\tresponse->len = 0;
\t\treturn;
\t}}
\tmemcpy(ptr, text, len);
\tresponse->ptr = ptr;
\tresponse->len = len;
}}

static void call_host(const char* method, const char* payload) {{
\tif (stored_host == NULL || stored_host->call == NULL || method == NULL) {{
\t\treturn;
\t}}
\tcliproxy_buffer response = {{0}};
\tconst uint8_t* request = (const uint8_t*)payload;
\tsize_t request_len = payload == NULL ? 0 : strlen(payload);
\tif (stored_host->call(stored_host->host_ctx, method, request, request_len, &response) == 0 && response.ptr != NULL && stored_host->free_buffer != NULL) {{
\t\tstored_host->free_buffer(response.ptr, response.len);
\t}}
}}

static int plugin_call(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {{
\tif (response != NULL) {{
\t\tresponse->ptr = NULL;
\t\tresponse->len = 0;
\t}}
\tif (method == NULL) {{
\t\twrite_response(response, "{{\\"ok\\":false,\\"error\\":{{\\"code\\":\\"invalid_method\\",\\"message\\":\\"method is required\\"}}}}");
\t\treturn 1;
\t}}
{chr(10).join(cases)}
\twrite_response(response, "{{\\"ok\\":false,\\"error\\":{{\\"code\\":\\"unknown_method\\",\\"message\\":\\"unknown method\\"}}}}");
\t(void)request;
\t(void)request_len;
\treturn 0;
}}

static void plugin_free(void* ptr, size_t len) {{
\t(void)len;
\tfree(ptr);
}}

static void plugin_shutdown(void) {{}}

CLIPROXY_EXPORT int cliproxy_plugin_init(const cliproxy_host_api* host, cliproxy_plugin_api* plugin) {{
\tif (plugin == NULL) {{
\t\treturn 1;
\t}}
\tstored_host = host;
\tplugin->abi_version = ABI_VERSION;
\tplugin->call = plugin_call;
\tplugin->free_buffer = plugin_free;
\tplugin->shutdown = plugin_shutdown;
\treturn 0;
}}
"""
    write(ROOT / slug / "c" / "CMakeLists.txt", cmake)
    write(ROOT / slug / "c" / "src" / "plugin.c", source)


def generate_rust(cap: Capability) -> None:
    slug = cap.slug
    ident = c_ident(slug)
    pid = plugin_id(cap, "rust")
    cases = []
    for method in methods_for(cap):
        result = envelope(result_for_method(cap, "rust", method))
        host_call = ""
        if slug == "host-callback" and method == "management.handle":
            host_call = f"""
            call_host("host.log", r#"{{"level":"info","message":"{pid} host callback log","fields":{{"plugin":"{pid}"}}}}"#);
            call_host("host.http.do", r#"{{"method":"GET","url":"https://example.com","headers":{{"user-agent":["{pid}"]}}}}"#);
"""
        cases.append(f'{json.dumps(method)} => {{{host_call}            write_response(response, {json.dumps(result)}); 0 }}')
    cargo = f"""[package]
name = "cliproxy-{slug}-rust"
version = "0.1.0"
edition = "2021"

[lib]
crate-type = ["cdylib"]
"""
    cargo_lock = f"""# This file is automatically @generated by Cargo.
# It is not intended for manual editing.
version = 4

[[package]]
name = "cliproxy-{slug}-rust"
version = "0.1.0"
"""
    source = f"""use std::ffi::CStr;
use std::os::raw::c_char;
use std::ptr;

const ABI_VERSION: u32 = {ABI_VERSION};

#[repr(C)]
pub struct CliproxyBuffer {{
    ptr: *mut u8,
    len: usize,
}}

type HostCall = unsafe extern "C" fn(*mut std::ffi::c_void, *const c_char, *const u8, usize, *mut CliproxyBuffer) -> i32;
type HostFree = unsafe extern "C" fn(*mut std::ffi::c_void, usize);
type PluginCall = unsafe extern "C" fn(*const c_char, *const u8, usize, *mut CliproxyBuffer) -> i32;
type PluginFree = unsafe extern "C" fn(*mut std::ffi::c_void, usize);
type PluginShutdown = unsafe extern "C" fn();

#[repr(C)]
pub struct CliproxyHostApi {{
    abi_version: u32,
    host_ctx: *mut std::ffi::c_void,
    call: Option<HostCall>,
    free_buffer: Option<HostFree>,
}}

#[repr(C)]
pub struct CliproxyPluginApi {{
    abi_version: u32,
    call: Option<PluginCall>,
    free_buffer: Option<PluginFree>,
    shutdown: Option<PluginShutdown>,
}}

static mut STORED_HOST: *const CliproxyHostApi = ptr::null();

#[no_mangle]
pub extern "C" fn cliproxy_plugin_init(host: *const CliproxyHostApi, plugin: *mut CliproxyPluginApi) -> i32 {{
    if plugin.is_null() {{
        return 1;
    }}
    unsafe {{
        STORED_HOST = host;
        (*plugin).abi_version = ABI_VERSION;
        (*plugin).call = Some(plugin_call);
        (*plugin).free_buffer = Some(plugin_free);
        (*plugin).shutdown = Some(plugin_shutdown);
    }}
    0
}}

unsafe extern "C" fn plugin_call(method: *const c_char, request: *const u8, request_len: usize, response: *mut CliproxyBuffer) -> i32 {{
    if !response.is_null() {{
        (*response).ptr = ptr::null_mut();
        (*response).len = 0;
    }}
    if method.is_null() {{
        write_response(response, r#"{{"ok":false,"error":{{"code":"invalid_method","message":"method is required"}}}}"#);
        return 1;
    }}
    let method = match CStr::from_ptr(method).to_str() {{
        Ok(value) => value,
        Err(_) => {{
            write_response(response, r#"{{"ok":false,"error":{{"code":"invalid_method","message":"method is not utf-8"}}}}"#);
            return 1;
        }}
    }};
    let _ = request;
    let _ = request_len;
    match method {{
        {",".join(cases)},
        _ => {{
            write_response(response, r#"{{"ok":false,"error":{{"code":"unknown_method","message":"unknown method"}}}}"#);
            0
        }}
    }}
}}

unsafe extern "C" fn plugin_free(ptr: *mut std::ffi::c_void, len: usize) {{
    if !ptr.is_null() {{
        let _ = Vec::from_raw_parts(ptr as *mut u8, len, len);
    }}
}}

unsafe extern "C" fn plugin_shutdown() {{}}

fn write_response(response: *mut CliproxyBuffer, text: &str) {{
    if response.is_null() {{
        return;
    }}
    let mut bytes = text.as_bytes().to_vec();
    let len = bytes.len();
    let ptr = bytes.as_mut_ptr();
    std::mem::forget(bytes);
    unsafe {{
        (*response).ptr = ptr;
        (*response).len = len;
    }}
}}

#[allow(dead_code)]
fn call_host(method: &str, payload: &str) {{
    unsafe {{
        if STORED_HOST.is_null() {{
            return;
        }}
        let host = &*STORED_HOST;
        let Some(call) = host.call else {{
            return;
        }};
        let mut method_bytes = method.as_bytes().to_vec();
        method_bytes.push(0);
        let mut response = CliproxyBuffer {{ ptr: ptr::null_mut(), len: 0 }};
        let rc = call(
            host.host_ctx,
            method_bytes.as_ptr() as *const c_char,
            payload.as_ptr(),
            payload.len(),
            &mut response,
        );
        if rc == 0 && !response.ptr.is_null() {{
            if let Some(free_buffer) = host.free_buffer {{
                free_buffer(response.ptr as *mut std::ffi::c_void, response.len);
            }}
        }}
    }}
}}
"""
    write(ROOT / slug / "rust" / "Cargo.toml", cargo)
    write(ROOT / slug / "rust" / "Cargo.lock", cargo_lock)
    write(ROOT / slug / "rust" / "src" / "lib.rs", source)


def main() -> None:
    for cap in CAPABILITIES:
        generate_go(cap)
        generate_c(cap)
        generate_rust(cap)


if __name__ == "__main__":
    main()
