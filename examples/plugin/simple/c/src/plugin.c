#include <ctype.h>
#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#if defined(_WIN32)
#define CLIPROXY_EXPORT __declspec(dllexport)
#else
#define CLIPROXY_EXPORT __attribute__((visibility("default")))
#endif

#define ABI_VERSION 1

typedef struct {
    void* ptr;
    size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
    uint32_t abi_version;
    void* host_ctx;
    cliproxy_host_call_fn call;
    cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
    uint32_t abi_version;
    cliproxy_plugin_call_fn call;
    cliproxy_plugin_free_fn free_buffer;
    cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

static long usage_count = 0;

static const char* REGISTRATION_RESPONSE =
    "{\"ok\":true,\"result\":{\"schema_version\":1,\"metadata\":{\"Name\":\"example-simple-c\","
    "\"Version\":\"0.1.0\",\"Author\":\"router-for-me\","
    "\"GitHubRepository\":\"https://github.com/router-for-me/CLIProxyAPI\","
    "\"Logo\":\"https://raw.githubusercontent.com/router-for-me/CLIProxyAPI/main/docs/logo.png\","
    "\"ConfigFields\":["
    "{\"Name\":\"config1\",\"Type\":\"boolean\",\"Description\":\"Enables the example boolean option.\"},"
    "{\"Name\":\"config2\",\"Type\":\"string\",\"Description\":\"Stores the example string option.\"},"
    "{\"Name\":\"config3\",\"Type\":\"integer\",\"Description\":\"Stores the example integer option.\"},"
    "{\"Name\":\"mode\",\"Type\":\"enum\",\"EnumValues\":[\"safe\",\"fast\"],"
    "\"Description\":\"Selects the example execution mode.\"}]},"
    "\"capabilities\":{\"model_registrar\":true,\"model_provider\":true,\"auth_provider\":true,"
    "\"frontend_auth_provider\":true,\"executor\":true,\"executor_model_scope\":\"both\","
    "\"executor_input_formats\":[\"chat-completions\"],"
    "\"executor_output_formats\":[\"chat-completions\"],\"request_translator\":true,"
    "\"request_normalizer\":true,\"response_translator\":true,\"response_before_translator\":true,"
    "\"response_after_translator\":true,\"thinking_applier\":true,\"usage_plugin\":true,"
    "\"command_line_plugin\":true,\"management_api\":true}}}";

static const char* MODEL_RESPONSE =
    "{\"ok\":true,\"result\":{\"Provider\":\"plugin-example-c\",\"Models\":[{\"ID\":\"plugin-example-c-model\","
    "\"Object\":\"model\",\"OwnedBy\":\"plugin-example-c\",\"DisplayName\":\"Plugin Example C Model\","
    "\"SupportedGenerationMethods\":[\"chat\"],\"ContextLength\":8192,"
    "\"MaxCompletionTokens\":1024,\"UserDefined\":true}]}}";

static const char* IDENTIFIER_RESPONSE = "{\"ok\":true,\"result\":{\"identifier\":\"plugin-example-c\"}}";
static const char* LOGIN_START_RESPONSE =
    "{\"ok\":true,\"result\":{\"Provider\":\"plugin-example-c\",\"URL\":\"https://example.invalid/plugin-login\","
    "\"State\":\"example-state\",\"ExpiresAt\":\"2030-01-01T00:00:00Z\"}}";
static const char* LOGIN_POLL_RESPONSE =
    "{\"ok\":true,\"result\":{\"Status\":\"error\",\"Message\":\"example plugin has no interactive login\"}}";
static const char* FRONTEND_AUTH_RESPONSE =
    "{\"ok\":true,\"result\":{\"Authenticated\":true,\"Principal\":\"plugin-example-c\","
    "\"Metadata\":{\"provider\":\"plugin-example-c\"}}}";
static const char* STREAM_RESPONSE =
    "{\"ok\":true,\"result\":{\"headers\":{\"content-type\":[\"text/event-stream\"]},"
    "\"chunks\":[{\"Payload\":\"cGx1Z2luLWV4YW1wbGUtYwo=\"}]}}";
static const char* CLI_REGISTER_RESPONSE =
    "{\"ok\":true,\"result\":{\"Flags\":[{\"Name\":\"plugin-example-c-command\","
    "\"Usage\":\"Run the example C ABI plugin command\",\"Type\":\"bool\"}]}}";
static const char* CLI_EXECUTE_RESPONSE =
    "{\"ok\":true,\"result\":{\"Stdout\":\"cGx1Z2luIGV4YW1wbGUgYyBjb21tYW5kCg==\",\"ExitCode\":0}}";
static const char* MANAGEMENT_REGISTER_RESPONSE =
    "{\"ok\":true,\"result\":{\"Resources\":[{\"Path\":\"/status\","
    "\"Menu\":\"Example C Plugin\",\"Description\":\"CPA exposes this menu resource under /v0/resource/plugins/example-c/status.\"}]}}";
static const char* UNKNOWN_METHOD_RESPONSE =
    "{\"ok\":false,\"error\":{\"code\":\"unknown_method\",\"message\":\"unknown method\"}}";
static const char* INVALID_METHOD_RESPONSE =
    "{\"ok\":false,\"error\":{\"code\":\"invalid_method\",\"message\":\"method is required\"}}";
static const char BASE64_TABLE[] = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

static char* format_string(const char* format, ...) {
    va_list args;
    va_start(args, format);
    va_list args_copy;
    va_copy(args_copy, args);
    int len = vsnprintf(NULL, 0, format, args);
    va_end(args);
    if (len < 0) {
        va_end(args_copy);
        return NULL;
    }
    char* out = (char*)malloc((size_t)len + 1);
    if (out == NULL) {
        va_end(args_copy);
        return NULL;
    }
    vsnprintf(out, (size_t)len + 1, format, args_copy);
    va_end(args_copy);
    return out;
}

static char* copy_request_string(const uint8_t* request, size_t request_len) {
    char* out = (char*)malloc(request_len + 1);
    if (out == NULL) {
        return NULL;
    }
    if (request_len > 0 && request != NULL) {
        memcpy(out, request, request_len);
    }
    out[request_len] = '\0';
    return out;
}

static char* json_escape(const char* value) {
    if (value == NULL) {
        return format_string("");
    }
    size_t len = strlen(value);
    char* out = (char*)malloc((len * 2) + 1);
    if (out == NULL) {
        return NULL;
    }
    size_t pos = 0;
    for (size_t i = 0; i < len; i++) {
        unsigned char c = (unsigned char)value[i];
        if (c == '"' || c == '\\') {
            out[pos++] = '\\';
            out[pos++] = (char)c;
        } else if (c == '\n') {
            out[pos++] = '\\';
            out[pos++] = 'n';
        } else if (c == '\r') {
            out[pos++] = '\\';
            out[pos++] = 'r';
        } else if (c == '\t') {
            out[pos++] = '\\';
            out[pos++] = 't';
        } else if (c < 0x20) {
            out[pos++] = ' ';
        } else {
            out[pos++] = (char)c;
        }
    }
    out[pos] = '\0';
    return out;
}

static char* base64_encode(const uint8_t* data, size_t len) {
    size_t out_len = ((len + 2) / 3) * 4;
    char* out = (char*)malloc(out_len + 1);
    if (out == NULL) {
        return NULL;
    }
    size_t i = 0;
    size_t j = 0;
    while (i < len) {
        uint32_t octet_a = i < len ? data[i++] : 0;
        uint32_t octet_b = i < len ? data[i++] : 0;
        uint32_t octet_c = i < len ? data[i++] : 0;
        uint32_t triple = (octet_a << 16) | (octet_b << 8) | octet_c;
        out[j++] = BASE64_TABLE[(triple >> 18) & 0x3F];
        out[j++] = BASE64_TABLE[(triple >> 12) & 0x3F];
        out[j++] = BASE64_TABLE[(triple >> 6) & 0x3F];
        out[j++] = BASE64_TABLE[triple & 0x3F];
    }
    if (len % 3 == 1) {
        out[out_len - 2] = '=';
        out[out_len - 1] = '=';
    } else if (len % 3 == 2) {
        out[out_len - 1] = '=';
    }
    out[out_len] = '\0';
    return out;
}

static int base64_value(char c) {
    if (c >= 'A' && c <= 'Z') {
        return c - 'A';
    }
    if (c >= 'a' && c <= 'z') {
        return c - 'a' + 26;
    }
    if (c >= '0' && c <= '9') {
        return c - '0' + 52;
    }
    if (c == '+') {
        return 62;
    }
    if (c == '/') {
        return 63;
    }
    return -1;
}

static uint8_t* base64_decode(const char* input, size_t* out_len) {
    size_t len = input == NULL ? 0 : strlen(input);
    uint8_t* out = (uint8_t*)malloc(((len * 3) / 4) + 4);
    if (out == NULL) {
        return NULL;
    }
    int value = 0;
    int bits = -8;
    size_t pos = 0;
    for (size_t i = 0; i < len; i++) {
        if (input[i] == '=') {
            break;
        }
        int digit = base64_value(input[i]);
        if (digit < 0) {
            continue;
        }
        value = (value << 6) | digit;
        bits += 6;
        if (bits >= 0) {
            out[pos++] = (uint8_t)((value >> bits) & 0xFF);
            bits -= 8;
        }
    }
    *out_len = pos;
    return out;
}

static char* extract_json_string(const char* json, const char* key) {
    char* pattern = format_string("\"%s\"", key);
    if (pattern == NULL || json == NULL) {
        free(pattern);
        return NULL;
    }
    const char* pos = json;
    size_t pattern_len = strlen(pattern);
    while ((pos = strstr(pos, pattern)) != NULL) {
        const char* p = pos + pattern_len;
        while (*p != '\0' && isspace((unsigned char)*p)) {
            p++;
        }
        if (*p++ != ':') {
            pos += pattern_len;
            continue;
        }
        while (*p != '\0' && isspace((unsigned char)*p)) {
            p++;
        }
        if (*p++ != '"') {
            pos += pattern_len;
            continue;
        }
        char* out = (char*)malloc(strlen(p) + 1);
        if (out == NULL) {
            free(pattern);
            return NULL;
        }
        size_t out_pos = 0;
        while (*p != '\0') {
            if (*p == '"') {
                out[out_pos] = '\0';
                free(pattern);
                return out;
            }
            if (*p == '\\' && p[1] != '\0') {
                p++;
                if (*p == 'n') {
                    out[out_pos++] = '\n';
                } else if (*p == 'r') {
                    out[out_pos++] = '\r';
                } else if (*p == 't') {
                    out[out_pos++] = '\t';
                } else {
                    out[out_pos++] = *p;
                }
            } else {
                out[out_pos++] = *p;
            }
            p++;
        }
        free(out);
        pos += pattern_len;
    }
    free(pattern);
    return NULL;
}

static long extract_json_int(const char* json, const char* key, long fallback) {
    char* pattern = format_string("\"%s\"", key);
    if (pattern == NULL || json == NULL) {
        free(pattern);
        return fallback;
    }
    const char* pos = strstr(json, pattern);
    free(pattern);
    if (pos == NULL) {
        return fallback;
    }
    const char* p = strchr(pos, ':');
    if (p == NULL) {
        return fallback;
    }
    p++;
    while (*p != '\0' && isspace((unsigned char)*p)) {
        p++;
    }
    char* end = NULL;
    long value = strtol(p, &end, 10);
    return end == p ? fallback : value;
}

static char* wrap_ok(const char* result_json) {
    return format_string("{\"ok\":true,\"result\":%s}", result_json == NULL ? "{}" : result_json);
}

static char* make_error(const char* code, const char* message) {
    char* escaped = json_escape(message);
    char* out = format_string("{\"ok\":false,\"error\":{\"code\":\"%s\",\"message\":\"%s\"}}", code, escaped == NULL ? "" : escaped);
    free(escaped);
    return out;
}

static char* make_auth_data(const uint8_t* request, size_t request_len) {
    char* storage = base64_encode(request == NULL ? (const uint8_t*)"" : request, request == NULL ? 0 : request_len);
    char* out = format_string(
        "{\"Provider\":\"plugin-example-c\",\"ID\":\"plugin-example-c\",\"FileName\":\"plugin-example-c.json\","
        "\"Label\":\"Plugin Example C\",\"StorageJSON\":\"%s\",\"Metadata\":{\"type\":\"plugin-example-c\"}}",
        storage == NULL ? "" : storage);
    free(storage);
    return out;
}

static char* make_auth_parse_response(const uint8_t* request, size_t request_len) {
    char* auth = make_auth_data(request, request_len);
    char* result = format_string("{\"Handled\":true,\"Auth\":%s}", auth == NULL ? "{}" : auth);
    char* out = wrap_ok(result);
    free(auth);
    free(result);
    return out;
}

static char* make_auth_refresh_response(const uint8_t* request, size_t request_len) {
    char* auth = make_auth_data(request, request_len);
    char* result = format_string("{\"Auth\":%s}", auth == NULL ? "{}" : auth);
    char* out = wrap_ok(result);
    free(auth);
    free(result);
    return out;
}

static char* make_payload_echo_response(const uint8_t* request, size_t request_len) {
    char* json = copy_request_string(request, request_len);
    char* body = extract_json_string(json, "Body");
    char* out = NULL;
    if (body == NULL) {
        out = make_error("invalid_request", "request body field is required");
    } else {
        char* result = format_string("{\"Body\":\"%s\"}", body);
        out = wrap_ok(result);
        free(result);
    }
    free(json);
    free(body);
    return out;
}

static char* make_executor_response(const uint8_t* request, size_t request_len) {
    char* json = copy_request_string(request, request_len);
    char* model = extract_json_string(json, "Model");
    char* format = extract_json_string(json, "Format");
    char* model_escaped = json_escape(model == NULL ? "plugin-example-c-model" : model);
    char* format_escaped = json_escape(format == NULL ? "chat-completions" : format);
    char* payload_json = format_string(
        "{\"id\":\"plugin-example-c\",\"object\":\"chat.completion\",\"model\":\"%s\",\"format\":\"%s\"}",
        model_escaped == NULL ? "" : model_escaped,
        format_escaped == NULL ? "" : format_escaped);
    char* payload = base64_encode((const uint8_t*)payload_json, payload_json == NULL ? 0 : strlen(payload_json));
    char* result = format_string("{\"Payload\":\"%s\",\"Headers\":{\"content-type\":[\"application/json\"]}}", payload == NULL ? "" : payload);
    char* out = wrap_ok(result);
    free(json);
    free(model);
    free(format);
    free(model_escaped);
    free(format_escaped);
    free(payload_json);
    free(payload);
    free(result);
    return out;
}

static char* make_count_tokens_response(const uint8_t* request, size_t request_len) {
    char* json = copy_request_string(request, request_len);
    char* payload = extract_json_string(json, "Payload");
    size_t decoded_len = 0;
    uint8_t* decoded = base64_decode(payload == NULL ? "" : payload, &decoded_len);
    long tokens = decoded_len == 0 ? 0 : (long)((decoded_len + 3) / 4);
    char* payload_json = format_string("{\"total_tokens\":%ld}", tokens);
    char* payload_b64 = base64_encode((const uint8_t*)payload_json, payload_json == NULL ? 0 : strlen(payload_json));
    char* result = format_string("{\"Payload\":\"%s\",\"Headers\":{\"content-type\":[\"application/json\"]}}", payload_b64 == NULL ? "" : payload_b64);
    char* out = wrap_ok(result);
    free(json);
    free(payload);
    free(decoded);
    free(payload_json);
    free(payload_b64);
    free(result);
    return out;
}

static char* make_http_response(const uint8_t* request, size_t request_len) {
    char* json = copy_request_string(request, request_len);
    char* method = extract_json_string(json, "Method");
    char* url = extract_json_string(json, "URL");
    char* path = extract_json_string(json, "Path");
    char* method_escaped = json_escape(method == NULL ? "GET" : method);
    char* target_escaped = json_escape(url != NULL ? url : (path == NULL ? "/v0/resource/plugins/example-c/status" : path));
    char* body_json = format_string(
        "{\"plugin\":\"example-c\",\"method\":\"%s\",\"target\":\"%s\"}",
        method_escaped == NULL ? "" : method_escaped,
        target_escaped == NULL ? "" : target_escaped);
    char* body = base64_encode((const uint8_t*)body_json, body_json == NULL ? 0 : strlen(body_json));
    char* result = format_string(
        "{\"StatusCode\":200,\"Headers\":{\"content-type\":[\"application/json\"]},\"Body\":\"%s\"}",
        body == NULL ? "" : body);
    char* out = wrap_ok(result);
    free(json);
    free(method);
    free(url);
    free(path);
    free(method_escaped);
    free(target_escaped);
    free(body_json);
    free(body);
    free(result);
    return out;
}

static char* inject_thinking(const uint8_t* body, size_t body_len, const char* mode, long budget, const char* level) {
    char* body_text = (char*)malloc(body_len + 1);
    if (body_text == NULL) {
        return NULL;
    }
    memcpy(body_text, body, body_len);
    body_text[body_len] = '\0';
    char* mode_escaped = json_escape(mode == NULL ? "" : mode);
    char* level_escaped = json_escape(level == NULL ? "" : level);
    size_t start = 0;
    while (body_text[start] != '\0' && isspace((unsigned char)body_text[start])) {
        start++;
    }
    size_t end = strlen(body_text);
    while (end > start && isspace((unsigned char)body_text[end - 1])) {
        end--;
    }
    char* out = NULL;
    if (end > start + 1 && body_text[start] == '{' && body_text[end - 1] == '}') {
        int has_fields = 0;
        for (size_t i = start + 1; i < end - 1; i++) {
            if (!isspace((unsigned char)body_text[i])) {
                has_fields = 1;
                break;
            }
        }
        out = format_string(
            "%.*s%s\"plugin_example_thinking\":{\"mode\":\"%s\",\"budget\":%ld,\"level\":\"%s\"}}",
            (int)(end - 1 - start),
            body_text + start,
            has_fields ? "," : "",
            mode_escaped == NULL ? "" : mode_escaped,
            budget,
            level_escaped == NULL ? "" : level_escaped);
    } else {
        char* escaped_body = json_escape(body_text);
        out = format_string(
            "{\"original_body\":\"%s\",\"plugin_example_thinking\":{\"mode\":\"%s\",\"budget\":%ld,\"level\":\"%s\"}}",
            escaped_body == NULL ? "" : escaped_body,
            mode_escaped == NULL ? "" : mode_escaped,
            budget,
            level_escaped == NULL ? "" : level_escaped);
        free(escaped_body);
    }
    free(body_text);
    free(mode_escaped);
    free(level_escaped);
    return out;
}

static char* make_thinking_response(const uint8_t* request, size_t request_len) {
    char* json = copy_request_string(request, request_len);
    char* body_b64 = extract_json_string(json, "Body");
    char* mode = extract_json_string(json, "Mode");
    char* level = extract_json_string(json, "Level");
    long budget = extract_json_int(json, "Budget", 0);
    size_t body_len = 0;
    uint8_t* body = base64_decode(body_b64 == NULL ? "e30=" : body_b64, &body_len);
    char* body_json = inject_thinking(body == NULL ? (const uint8_t*)"{}" : body, body == NULL ? 2 : body_len, mode, budget, level);
    char* out_b64 = base64_encode((const uint8_t*)body_json, body_json == NULL ? 0 : strlen(body_json));
    char* result = format_string("{\"Body\":\"%s\"}", out_b64 == NULL ? "" : out_b64);
    char* out = wrap_ok(result);
    free(json);
    free(body_b64);
    free(mode);
    free(level);
    free(body);
    free(body_json);
    free(out_b64);
    free(result);
    return out;
}

static char* make_usage_response(void) {
    usage_count++;
    char* result = format_string("{\"Count\":%ld}", usage_count);
    char* out = wrap_ok(result);
    free(result);
    return out;
}

static void write_response(cliproxy_buffer* response, const char* text) {
    if (response == NULL || text == NULL) {
        return;
    }
    size_t len = strlen(text);
    void* ptr = malloc(len);
    if (ptr == NULL) {
        response->ptr = NULL;
        response->len = 0;
        return;
    }
    memcpy(ptr, text, len);
    response->ptr = ptr;
    response->len = len;
}

static int plugin_call(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
    if (response != NULL) {
        response->ptr = NULL;
        response->len = 0;
    }
    if (method == NULL) {
        write_response(response, INVALID_METHOD_RESPONSE);
        return 1;
    }
    const char* static_response = NULL;
    char* dynamic_response = NULL;
    if (strcmp(method, "plugin.register") == 0 || strcmp(method, "plugin.reconfigure") == 0) {
        static_response = REGISTRATION_RESPONSE;
    } else if (strcmp(method, "model.register") == 0 || strcmp(method, "model.static") == 0 || strcmp(method, "model.for_auth") == 0) {
        static_response = MODEL_RESPONSE;
    } else if (strcmp(method, "auth.identifier") == 0 || strcmp(method, "frontend_auth.identifier") == 0 || strcmp(method, "executor.identifier") == 0 || strcmp(method, "thinking.identifier") == 0) {
        static_response = IDENTIFIER_RESPONSE;
    } else if (strcmp(method, "auth.parse") == 0) {
        dynamic_response = make_auth_parse_response(request, request_len);
    } else if (strcmp(method, "auth.login.start") == 0) {
        static_response = LOGIN_START_RESPONSE;
    } else if (strcmp(method, "auth.login.poll") == 0) {
        static_response = LOGIN_POLL_RESPONSE;
    } else if (strcmp(method, "auth.refresh") == 0) {
        dynamic_response = make_auth_refresh_response(request, request_len);
    } else if (strcmp(method, "frontend_auth.authenticate") == 0) {
        static_response = FRONTEND_AUTH_RESPONSE;
    } else if (strcmp(method, "executor.execute") == 0) {
        dynamic_response = make_executor_response(request, request_len);
    } else if (strcmp(method, "executor.execute_stream") == 0) {
        static_response = STREAM_RESPONSE;
    } else if (strcmp(method, "executor.count_tokens") == 0) {
        dynamic_response = make_count_tokens_response(request, request_len);
    } else if (strcmp(method, "executor.http_request") == 0 || strcmp(method, "management.handle") == 0) {
        dynamic_response = make_http_response(request, request_len);
    } else if (strcmp(method, "request.translate") == 0 || strcmp(method, "request.normalize") == 0 || strcmp(method, "response.translate") == 0 || strcmp(method, "response.normalize_before") == 0 || strcmp(method, "response.normalize_after") == 0) {
        dynamic_response = make_payload_echo_response(request, request_len);
    } else if (strcmp(method, "thinking.apply") == 0) {
        dynamic_response = make_thinking_response(request, request_len);
    } else if (strcmp(method, "usage.handle") == 0) {
        dynamic_response = make_usage_response();
    } else if (strcmp(method, "command_line.register") == 0) {
        static_response = CLI_REGISTER_RESPONSE;
    } else if (strcmp(method, "command_line.execute") == 0) {
        static_response = CLI_EXECUTE_RESPONSE;
    } else if (strcmp(method, "management.register") == 0) {
        static_response = MANAGEMENT_REGISTER_RESPONSE;
    } else {
        static_response = UNKNOWN_METHOD_RESPONSE;
    }
    write_response(response, dynamic_response != NULL ? dynamic_response : static_response);
    free(dynamic_response);
    return 0;
}

static void plugin_free(void* ptr, size_t len) {
    (void)len;
    free(ptr);
}

static void plugin_shutdown(void) {}

CLIPROXY_EXPORT int cliproxy_plugin_init(const cliproxy_host_api* host, cliproxy_plugin_api* plugin) {
    if (plugin == NULL) {
        return 1;
    }
    (void)host;
    plugin->abi_version = ABI_VERSION;
    plugin->call = plugin_call;
    plugin->free_buffer = plugin_free;
    plugin->shutdown = plugin_shutdown;
    return 0;
}
