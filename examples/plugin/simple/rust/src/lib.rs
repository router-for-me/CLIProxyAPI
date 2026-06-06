use std::borrow::Cow;
use std::ffi::CStr;
use std::os::raw::c_char;
use std::ptr;
use std::sync::atomic::{AtomicI64, Ordering};

const ABI_VERSION: u32 = 1;
const BASE64_TABLE: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

static USAGE_COUNT: AtomicI64 = AtomicI64::new(0);

const REGISTRATION_RESPONSE: &str = r#"{"ok":true,"result":{"schema_version":1,"metadata":{"Name":"example-simple-rust","Version":"0.1.0","Author":"router-for-me","GitHubRepository":"https://github.com/router-for-me/CLIProxyAPI","Logo":"https://raw.githubusercontent.com/router-for-me/CLIProxyAPI/main/docs/logo.png","ConfigFields":[{"Name":"config1","Type":"boolean","Description":"Enables the example boolean option."},{"Name":"config2","Type":"string","Description":"Stores the example string option."},{"Name":"config3","Type":"integer","Description":"Stores the example integer option."},{"Name":"mode","Type":"enum","EnumValues":["safe","fast"],"Description":"Selects the example execution mode."}]},"capabilities":{"model_registrar":true,"model_provider":true,"auth_provider":true,"frontend_auth_provider":true,"executor":true,"executor_model_scope":"both","executor_input_formats":["chat-completions"],"executor_output_formats":["chat-completions"],"request_translator":true,"request_normalizer":true,"response_translator":true,"response_before_translator":true,"response_after_translator":true,"thinking_applier":true,"usage_plugin":true,"command_line_plugin":true,"management_api":true}}}"#;
const MODEL_RESPONSE: &str = r#"{"ok":true,"result":{"Provider":"plugin-example-rust","Models":[{"ID":"plugin-example-rust-model","Object":"model","OwnedBy":"plugin-example-rust","DisplayName":"Plugin Example Rust Model","SupportedGenerationMethods":["chat"],"ContextLength":8192,"MaxCompletionTokens":1024,"UserDefined":true}]}}"#;
const IDENTIFIER_RESPONSE: &str = r#"{"ok":true,"result":{"identifier":"plugin-example-rust"}}"#;
const LOGIN_START_RESPONSE: &str = r#"{"ok":true,"result":{"Provider":"plugin-example-rust","URL":"https://example.invalid/plugin-login","State":"example-state","ExpiresAt":"2030-01-01T00:00:00Z"}}"#;
const LOGIN_POLL_RESPONSE: &str = r#"{"ok":true,"result":{"Status":"error","Message":"example plugin has no interactive login"}}"#;
const FRONTEND_AUTH_RESPONSE: &str = r#"{"ok":true,"result":{"Authenticated":true,"Principal":"plugin-example-rust","Metadata":{"provider":"plugin-example-rust"}}}"#;
const STREAM_RESPONSE: &str = r#"{"ok":true,"result":{"headers":{"content-type":["text/event-stream"]},"chunks":[{"Payload":"cGx1Z2luLWV4YW1wbGUtcnVzdAo="}]}}"#;
const CLI_REGISTER_RESPONSE: &str = r#"{"ok":true,"result":{"Flags":[{"Name":"plugin-example-rust-command","Usage":"Run the example Rust ABI plugin command","Type":"bool"}]}}"#;
const CLI_EXECUTE_RESPONSE: &str = r#"{"ok":true,"result":{"Stdout":"cGx1Z2luIGV4YW1wbGUgcnVzdCBjb21tYW5kCg==","ExitCode":0}}"#;
const MANAGEMENT_REGISTER_RESPONSE: &str = r#"{"ok":true,"result":{"Routes":[{"Method":"GET","Path":"/plugins/example-rust/status","Menu":"Example Rust Plugin","Description":"Shows example Rust plugin status."}]}}"#;
const UNKNOWN_METHOD_RESPONSE: &str = r#"{"ok":false,"error":{"code":"unknown_method","message":"unknown method"}}"#;
const INVALID_METHOD_RESPONSE: &str = r#"{"ok":false,"error":{"code":"invalid_method","message":"method is required"}}"#;

#[repr(C)]
pub struct CliproxyBuffer {
    ptr: *mut u8,
    len: usize,
}

type HostCall = unsafe extern "C" fn(*mut std::ffi::c_void, *const c_char, *const u8, usize, *mut CliproxyBuffer) -> i32;
type HostFree = unsafe extern "C" fn(*mut std::ffi::c_void, usize);
type PluginCall = unsafe extern "C" fn(*const c_char, *const u8, usize, *mut CliproxyBuffer) -> i32;
type PluginFree = unsafe extern "C" fn(*mut std::ffi::c_void, usize);
type PluginShutdown = unsafe extern "C" fn();

#[repr(C)]
pub struct CliproxyHostApi {
    abi_version: u32,
    host_ctx: *mut std::ffi::c_void,
    call: Option<HostCall>,
    free_buffer: Option<HostFree>,
}

#[repr(C)]
pub struct CliproxyPluginApi {
    abi_version: u32,
    call: Option<PluginCall>,
    free_buffer: Option<PluginFree>,
    shutdown: Option<PluginShutdown>,
}

#[no_mangle]
pub extern "C" fn cliproxy_plugin_init(host: *const CliproxyHostApi, plugin: *mut CliproxyPluginApi) -> i32 {
    if plugin.is_null() {
        return 1;
    }
    unsafe {
        (*plugin).abi_version = ABI_VERSION;
        (*plugin).call = Some(plugin_call);
        (*plugin).free_buffer = Some(plugin_free);
        (*plugin).shutdown = Some(plugin_shutdown);
    }
    let _ = host;
    0
}

unsafe extern "C" fn plugin_call(method: *const c_char, request: *const u8, request_len: usize, response: *mut CliproxyBuffer) -> i32 {
    if !response.is_null() {
        (*response).ptr = ptr::null_mut();
        (*response).len = 0;
    }
    if method.is_null() {
        write_response(response, INVALID_METHOD_RESPONSE);
        return 1;
    }
    let method = match CStr::from_ptr(method).to_str() {
        Ok(value) => value,
        Err(_) => {
            write_response(response, r#"{"ok":false,"error":{"code":"invalid_method","message":"method is not utf-8"}}"#);
            return 1;
        }
    };
    let request = if request.is_null() || request_len == 0 {
        &[]
    } else {
        std::slice::from_raw_parts(request, request_len)
    };
    let response_text = handle_method(method, request);
    write_response(response, response_text.as_ref());
    0
}

fn handle_method(method: &str, request: &[u8]) -> Cow<'static, str> {
    match method {
        "plugin.register" | "plugin.reconfigure" => Cow::Borrowed(REGISTRATION_RESPONSE),
        "model.register" | "model.static" | "model.for_auth" => Cow::Borrowed(MODEL_RESPONSE),
        "auth.identifier" | "frontend_auth.identifier" | "executor.identifier" | "thinking.identifier" => Cow::Borrowed(IDENTIFIER_RESPONSE),
        "auth.parse" => Cow::Owned(make_auth_parse_response(request)),
        "auth.login.start" => Cow::Borrowed(LOGIN_START_RESPONSE),
        "auth.login.poll" => Cow::Borrowed(LOGIN_POLL_RESPONSE),
        "auth.refresh" => Cow::Owned(make_auth_refresh_response(request)),
        "frontend_auth.authenticate" => Cow::Borrowed(FRONTEND_AUTH_RESPONSE),
        "executor.execute" => Cow::Owned(make_executor_response(request)),
        "executor.execute_stream" => Cow::Borrowed(STREAM_RESPONSE),
        "executor.count_tokens" => Cow::Owned(make_count_tokens_response(request)),
        "executor.http_request" | "management.handle" => Cow::Owned(make_http_response(request)),
        "request.translate" | "request.normalize" | "response.translate" | "response.normalize_before" | "response.normalize_after" => Cow::Owned(make_payload_echo_response(request)),
        "thinking.apply" => Cow::Owned(make_thinking_response(request)),
        "usage.handle" => Cow::Owned(make_usage_response()),
        "command_line.register" => Cow::Borrowed(CLI_REGISTER_RESPONSE),
        "command_line.execute" => Cow::Borrowed(CLI_EXECUTE_RESPONSE),
        "management.register" => Cow::Borrowed(MANAGEMENT_REGISTER_RESPONSE),
        _ => Cow::Borrowed(UNKNOWN_METHOD_RESPONSE),
    }
}

fn make_auth_data(request: &[u8]) -> String {
    format!(
        r#"{{"Provider":"plugin-example-rust","ID":"plugin-example-rust","FileName":"plugin-example-rust.json","Label":"Plugin Example Rust","StorageJSON":"{}","Metadata":{{"type":"plugin-example-rust"}}}}"#,
        base64_encode(request),
    )
}

fn make_auth_parse_response(request: &[u8]) -> String {
    wrap_ok(&format!(r#"{{"Handled":true,"Auth":{}}}"#, make_auth_data(request)))
}

fn make_auth_refresh_response(request: &[u8]) -> String {
    wrap_ok(&format!(r#"{{"Auth":{}}}"#, make_auth_data(request)))
}

fn make_payload_echo_response(request: &[u8]) -> String {
    let json = String::from_utf8_lossy(request);
    match extract_json_string(&json, "Body") {
        Some(body) => wrap_ok(&format!(r#"{{"Body":"{}"}}"#, body)),
        None => make_error("invalid_request", "request body field is required"),
    }
}

fn make_executor_response(request: &[u8]) -> String {
    let json = String::from_utf8_lossy(request);
    let model = extract_json_string(&json, "Model").unwrap_or_else(|| "plugin-example-rust-model".to_string());
    let format = extract_json_string(&json, "Format").unwrap_or_else(|| "chat-completions".to_string());
    let payload = format!(
        r#"{{"id":"plugin-example-rust","object":"chat.completion","model":"{}","format":"{}"}}"#,
        json_escape(&model),
        json_escape(&format),
    );
    wrap_ok(&format!(
        r#"{{"Payload":"{}","Headers":{{"content-type":["application/json"]}}}}"#,
        base64_encode(payload.as_bytes()),
    ))
}

fn make_count_tokens_response(request: &[u8]) -> String {
    let json = String::from_utf8_lossy(request);
    let payload = extract_json_string(&json, "Payload").unwrap_or_default();
    let decoded = base64_decode(&payload);
    let tokens = if decoded.is_empty() { 0 } else { (decoded.len() + 3) / 4 };
    let payload_json = format!(r#"{{"total_tokens":{}}}"#, tokens);
    wrap_ok(&format!(
        r#"{{"Payload":"{}","Headers":{{"content-type":["application/json"]}}}}"#,
        base64_encode(payload_json.as_bytes()),
    ))
}

fn make_http_response(request: &[u8]) -> String {
    let json = String::from_utf8_lossy(request);
    let method = extract_json_string(&json, "Method").unwrap_or_else(|| "GET".to_string());
    let target = extract_json_string(&json, "URL")
        .or_else(|| extract_json_string(&json, "Path"))
        .unwrap_or_else(|| "/plugins/example-rust/status".to_string());
    let body = format!(
        r#"{{"plugin":"example-rust","method":"{}","target":"{}"}}"#,
        json_escape(&method),
        json_escape(&target),
    );
    wrap_ok(&format!(
        r#"{{"StatusCode":200,"Headers":{{"content-type":["application/json"]}},"Body":"{}"}}"#,
        base64_encode(body.as_bytes()),
    ))
}

fn make_thinking_response(request: &[u8]) -> String {
    let json = String::from_utf8_lossy(request);
    let body_b64 = extract_json_string(&json, "Body").unwrap_or_else(|| "e30=".to_string());
    let body = base64_decode(&body_b64);
    let mode = extract_json_string(&json, "Mode").unwrap_or_default();
    let level = extract_json_string(&json, "Level").unwrap_or_default();
    let budget = extract_json_int(&json, "Budget").unwrap_or(0);
    let rewritten = inject_thinking(&body, &mode, budget, &level);
    wrap_ok(&format!(r#"{{"Body":"{}"}}"#, base64_encode(rewritten.as_bytes())))
}

fn make_usage_response() -> String {
    let count = USAGE_COUNT.fetch_add(1, Ordering::SeqCst) + 1;
    wrap_ok(&format!(r#"{{"Count":{}}}"#, count))
}

fn inject_thinking(body: &[u8], mode: &str, budget: i64, level: &str) -> String {
    let body_text = String::from_utf8_lossy(body);
    let trimmed = body_text.trim();
    let thinking = format!(
        r#""plugin_example_thinking":{{"mode":"{}","budget":{},"level":"{}"}}"#,
        json_escape(mode),
        budget,
        json_escape(level),
    );
    if trimmed.starts_with('{') && trimmed.ends_with('}') {
        let inner = &trimmed[1..trimmed.len() - 1];
        if inner.trim().is_empty() {
            format!("{{{}}}", thinking)
        } else {
            format!("{{{},{} }}", inner, thinking)
        }
    } else {
        format!(
            r#"{{"original_body":"{}","plugin_example_thinking":{{"mode":"{}","budget":{},"level":"{}"}}}}"#,
            json_escape(&body_text),
            json_escape(mode),
            budget,
            json_escape(level),
        )
    }
}

fn wrap_ok(result_json: &str) -> String {
    format!(r#"{{"ok":true,"result":{}}}"#, result_json)
}

fn make_error(code: &str, message: &str) -> String {
    format!(
        r#"{{"ok":false,"error":{{"code":"{}","message":"{}"}}}}"#,
        json_escape(code),
        json_escape(message),
    )
}

fn extract_json_string(json: &str, key: &str) -> Option<String> {
    let pattern = format!(r#""{}""#, key);
    let bytes = json.as_bytes();
    let mut start = 0;
    while let Some(relative) = json[start..].find(&pattern) {
        let mut i = start + relative + pattern.len();
        while i < bytes.len() && bytes[i].is_ascii_whitespace() {
            i += 1;
        }
        if i >= bytes.len() || bytes[i] != b':' {
            start = i.saturating_add(1);
            continue;
        }
        i += 1;
        while i < bytes.len() && bytes[i].is_ascii_whitespace() {
            i += 1;
        }
        if i >= bytes.len() || bytes[i] != b'"' {
            start = i.saturating_add(1);
            continue;
        }
        i += 1;
        let mut out = Vec::new();
        while i < bytes.len() {
            if bytes[i] == b'"' {
                return Some(String::from_utf8_lossy(&out).into_owned());
            }
            if bytes[i] == b'\\' && i + 1 < bytes.len() {
                i += 1;
                match bytes[i] {
                    b'n' => out.push(b'\n'),
                    b'r' => out.push(b'\r'),
                    b't' => out.push(b'\t'),
                    other => out.push(other),
                }
            } else {
                out.push(bytes[i]);
            }
            i += 1;
        }
        start = i;
    }
    None
}

fn extract_json_int(json: &str, key: &str) -> Option<i64> {
    let pattern = format!(r#""{}""#, key);
    let idx = json.find(&pattern)?;
    let bytes = json.as_bytes();
    let mut i = idx + pattern.len();
    while i < bytes.len() && bytes[i].is_ascii_whitespace() {
        i += 1;
    }
    if i >= bytes.len() || bytes[i] != b':' {
        return None;
    }
    i += 1;
    while i < bytes.len() && bytes[i].is_ascii_whitespace() {
        i += 1;
    }
    let start = i;
    if i < bytes.len() && bytes[i] == b'-' {
        i += 1;
    }
    while i < bytes.len() && bytes[i].is_ascii_digit() {
        i += 1;
    }
    json[start..i].parse().ok()
}

fn json_escape(value: &str) -> String {
    let mut out = String::with_capacity(value.len());
    for ch in value.chars() {
        match ch {
            '"' => out.push_str("\\\""),
            '\\' => out.push_str("\\\\"),
            '\n' => out.push_str("\\n"),
            '\r' => out.push_str("\\r"),
            '\t' => out.push_str("\\t"),
            ch if ch.is_control() => out.push(' '),
            ch => out.push(ch),
        }
    }
    out
}

fn base64_encode(data: &[u8]) -> String {
    let mut out = String::with_capacity(((data.len() + 2) / 3) * 4);
    let mut i = 0;
    while i < data.len() {
        let a = data[i] as u32;
        i += 1;
        let b = if i < data.len() { data[i] as u32 } else { 0 };
        i += 1;
        let c = if i < data.len() { data[i] as u32 } else { 0 };
        i += 1;
        let triple = (a << 16) | (b << 8) | c;
        out.push(BASE64_TABLE[((triple >> 18) & 0x3F) as usize] as char);
        out.push(BASE64_TABLE[((triple >> 12) & 0x3F) as usize] as char);
        out.push(BASE64_TABLE[((triple >> 6) & 0x3F) as usize] as char);
        out.push(BASE64_TABLE[(triple & 0x3F) as usize] as char);
    }
    match data.len() % 3 {
        1 => {
            out.pop();
            out.pop();
            out.push('=');
            out.push('=');
        }
        2 => {
            out.pop();
            out.push('=');
        }
        _ => {}
    }
    out
}

fn base64_decode(input: &str) -> Vec<u8> {
    let mut out = Vec::with_capacity((input.len() * 3) / 4);
    let mut value: i32 = 0;
    let mut bits = -8;
    for byte in input.bytes() {
        if byte == b'=' {
            break;
        }
        let digit = match byte {
            b'A'..=b'Z' => byte - b'A',
            b'a'..=b'z' => byte - b'a' + 26,
            b'0'..=b'9' => byte - b'0' + 52,
            b'+' => 62,
            b'/' => 63,
            _ => continue,
        } as i32;
        value = (value << 6) | digit;
        bits += 6;
        if bits >= 0 {
            out.push(((value >> bits) & 0xFF) as u8);
            bits -= 8;
        }
    }
    out
}

unsafe extern "C" fn plugin_free(ptr: *mut std::ffi::c_void, len: usize) {
    if !ptr.is_null() {
        let _ = Vec::from_raw_parts(ptr as *mut u8, len, len);
    }
}

unsafe extern "C" fn plugin_shutdown() {}

fn write_response(response: *mut CliproxyBuffer, text: &str) {
    if response.is_null() {
        return;
    }
    let mut bytes = text.as_bytes().to_vec();
    let len = bytes.len();
    let ptr = bytes.as_mut_ptr();
    std::mem::forget(bytes);
    unsafe {
        (*response).ptr = ptr;
        (*response).len = len;
    }
}
