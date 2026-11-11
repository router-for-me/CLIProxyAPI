use std::ffi::CStr;
use std::os::raw::c_char;
use std::ptr;

const ABI_VERSION: u32 = 1;

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

static mut STORED_HOST: *const CliproxyHostApi = ptr::null();

#[no_mangle]
pub extern "C" fn cliproxy_plugin_init(host: *const CliproxyHostApi, plugin: *mut CliproxyPluginApi) -> i32 {
    if plugin.is_null() {
        return 1;
    }
    unsafe {
        STORED_HOST = host;
        (*plugin).abi_version = ABI_VERSION;
        (*plugin).call = Some(plugin_call);
        (*plugin).free_buffer = Some(plugin_free);
        (*plugin).shutdown = Some(plugin_shutdown);
    }
    0
}

unsafe extern "C" fn plugin_call(method: *const c_char, request: *const u8, request_len: usize, response: *mut CliproxyBuffer) -> i32 {
    if !response.is_null() {
        (*response).ptr = ptr::null_mut();
        (*response).len = 0;
    }
    if method.is_null() {
        write_response(response, r#"{"ok":false,"error":{"code":"invalid_method","message":"method is required"}}"#);
        return 1;
    }
    let method = match CStr::from_ptr(method).to_str() {
        Ok(value) => value,
        Err(_) => {
            write_response(response, r#"{"ok":false,"error":{"code":"invalid_method","message":"method is not utf-8"}}"#);
            return 1;
        }
    };
    let _ = request;
    let _ = request_len;
    match method {
        "plugin.register" => {            write_response(response, "{\"ok\":true,\"result\":{\"schema_version\":1,\"metadata\":{\"Name\":\"example-host-callback-rust\",\"Version\":\"0.1.0\",\"Author\":\"router-for-me\",\"GitHubRepository\":\"https://github.com/router-for-me/CLIProxyAPI\",\"Logo\":\"https://example.invalid/example-host-callback-rust.png\",\"ConfigFields\":[]},\"capabilities\":{\"management_api\":true}}}"); 0 },"plugin.reconfigure" => {            write_response(response, "{\"ok\":true,\"result\":{\"schema_version\":1,\"metadata\":{\"Name\":\"example-host-callback-rust\",\"Version\":\"0.1.0\",\"Author\":\"router-for-me\",\"GitHubRepository\":\"https://github.com/router-for-me/CLIProxyAPI\",\"Logo\":\"https://example.invalid/example-host-callback-rust.png\",\"ConfigFields\":[]},\"capabilities\":{\"management_api\":true}}}"); 0 },"management.register" => {            write_response(response, "{\"ok\":true,\"result\":{\"resources\":[{\"Path\":\"/status\",\"Menu\":\"Host Callback\",\"Description\":\"CPA exposes this menu resource under /v0/resource/plugins/example-host-callback-rust/status.\"}]}}"); 0 },"management.handle" => {
            call_host("host.log", r#"{"level":"info","message":"example-host-callback-rust host callback log","fields":{"plugin":"example-host-callback-rust"}}"#);
            call_host("host.http.do", r#"{"method":"GET","url":"https://example.com","headers":{"user-agent":["example-host-callback-rust"]}}"#);
            write_response(response, "{\"ok\":true,\"result\":{\"StatusCode\":200,\"Headers\":{\"content-type\":[\"text/html; charset=utf-8\"]},\"Body\":\"PCFkb2N0eXBlIGh0bWw+PHRpdGxlPkhvc3QgQ2FsbGJhY2s8L3RpdGxlPjxtYWluPkhvc3QgQ2FsbGJhY2sgcmVzb3VyY2U8L21haW4+\"}}"); 0 },
        _ => {
            write_response(response, r#"{"ok":false,"error":{"code":"unknown_method","message":"unknown method"}}"#);
            0
        }
    }
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

#[allow(dead_code)]
fn call_host(method: &str, payload: &str) {
    unsafe {
        if STORED_HOST.is_null() {
            return;
        }
        let host = &*STORED_HOST;
        let Some(call) = host.call else {
            return;
        };
        let mut method_bytes = method.as_bytes().to_vec();
        method_bytes.push(0);
        let mut response = CliproxyBuffer { ptr: ptr::null_mut(), len: 0 };
        let rc = call(
            host.host_ctx,
            method_bytes.as_ptr() as *const c_char,
            payload.as_ptr(),
            payload.len(),
            &mut response,
        );
        if rc == 0 && !response.ptr.is_null() {
            if let Some(free_buffer) = host.free_buffer {
                free_buffer(response.ptr as *mut std::ffi::c_void, response.len);
            }
        }
    }
}
