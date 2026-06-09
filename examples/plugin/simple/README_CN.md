# 标准动态库插件示例

这是混合全部能力的完整骨架示例。单能力示例请查看 `../README_CN.md`。

本目录是当前标准动态库插件 ABI 的参考骨架。ABI 与语言无关：宿主加载原生动态库，调用 `cliproxy_plugin_init`，然后通过稳定的 C 函数表交换 JSON 信封。

本目录包含同一个混合能力示例的 Go、C、Rust 三种完整实现。Go 示例使用 `-buildmode=c-shared`，C 示例使用 CMake，Rust 示例使用 `cdylib` crate。

## 入口

每个插件必须导出：

```c
int cliproxy_plugin_init(const cliproxy_host_api* host, cliproxy_plugin_api* plugin);
```

插件填充 `cliproxy_plugin_api`：

```c
int call(char* method, uint8_t* request, size_t request_len, cliproxy_buffer* response);
void free_buffer(void* ptr, size_t len);
void shutdown(void);
```

宿主提供 `cliproxy_host_api`：

```c
int call(void* host_ctx, char* method, uint8_t* request, size_t request_len, cliproxy_buffer* response);
void free_buffer(void* ptr, size_t len);
```

C ABI 不传递 Go interface、Go slice、Go map、Go channel、`context.Context` 或 Go error。

## JSON 信封

成功响应：

```json
{
  "ok": true,
  "result": {}
}
```

错误响应：

```json
{
  "ok": false,
  "error": {
    "code": "invalid_request",
    "message": "request is invalid"
  }
}
```

原始字节字段通过 JSON 自动使用 base64 编码。

## 能力

`plugin.register` 和 `plugin.reconfigure` 返回 metadata 和能力开关。本示例声明完整的提供方插件能力：

- 模型提供方
- 模型注册器
- 认证提供方
- 前端认证提供方
- 执行器
- 请求和响应转换
- 思考配置处理
- 用量观察
- 命令行插件
- Management API 插件

宿主保留现有优先级规则：原生逻辑优先，插件补齐缺口，高优先级插件先于低优先级插件执行。

## 目录布局

- `go/`：完整混合能力 Go 实现。
- `c/`：完整混合能力 C 实现，不依赖外部库。
- `rust/`：完整混合能力 Rust 实现，不依赖外部库。

三种实现都会在需要请求内容的方法中解析传入 JSON。认证方法会把原始请求作为 `StorageJSON`，请求和响应转换会回显传入 `Body`，Thinking 会解码 `Body` 并追加 `plugin_example_thinking`，执行器方法会使用 `Model`、`Format`、`Payload` 等请求字段，Usage 会维护进程内计数。

## 构建

在仓库根目录构建。

构建全部插件示例，包括 `simple` 的三种语言实现：

```bash
make -C examples/plugin build
```

产物会写入 `examples/plugin/bin`，当前平台扩展名下分别为 `simple-go`、`simple-c`、`simple-rust`。

macOS 手动构建 Go：

```bash
mkdir -p plugins/darwin/$(go env GOARCH)
go build -buildmode=c-shared -o plugins/darwin/$(go env GOARCH)/simple-go.dylib ./examples/plugin/simple/go
rm -f plugins/darwin/$(go env GOARCH)/simple-go.h
```

macOS 手动构建 C：

```bash
mkdir -p plugins/darwin/$(go env GOARCH)
cmake -S examples/plugin/simple/c -B /tmp/cliproxy-simple-c-build -DCMAKE_LIBRARY_OUTPUT_DIRECTORY=$PWD/plugins/darwin/$(go env GOARCH)
cmake --build /tmp/cliproxy-simple-c-build
```

macOS 手动构建 Rust：

```bash
mkdir -p plugins/darwin/$(go env GOARCH)
cd examples/plugin/simple/rust
CARGO_TARGET_DIR=/tmp/cliproxy-simple-rust-target cargo build --release --locked
cp /tmp/cliproxy-simple-rust-target/release/libcliproxy_simple_rust.dylib ../../../../plugins/darwin/$(go env GOARCH)/simple-rust.dylib
```

Linux、FreeBSD 或 Windows 使用相同源码目录，平台扩展名以 `examples/plugin/Makefile` 的规则为准。

插件 ID 来自动态库文件名去掉平台扩展名。通过 Makefile 构建的产物分别对应 `plugins.configs.simple-go`、`plugins.configs.simple-c` 和 `plugins.configs.simple-rust`。

## 发现规则

宿主搜索：

```text
plugins/<GOOS>/<GOARCH>-<variant>
plugins/<GOOS>/<GOARCH>
plugins
```

支持的扩展名：

- Linux 和 FreeBSD 使用 `.so`
- macOS 使用 `.dylib`
- Windows 使用 `.dll`

插件 ID 必须匹配：

```text
[A-Za-z0-9][A-Za-z0-9._-]{0,127}
```

## 配置

动态插件默认关闭。

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    simple-go:
      enabled: true
      priority: 1
      config1: true
      config2: "string"
      config3: 3
      mode: "safe"
```

`plugins.configs.<pluginID>` 会作为标准化 YAML 字节放进 JSON 请求，传给 `plugin.register` 或 `plugin.reconfigure`。

## 宿主 HTTP 桥接

插件可以通过 `host.call` 调用宿主能力。HTTP 桥接方法是：

```text
host.http.do
```

真实 HTTP 请求仍由宿主执行，因此代理、传输策略、认证上下文和请求日志仍由宿主控制。

## Management API

原生插件管理接口保持不变：

```text
GET /v0/management/plugins
PATCH /v0/management/plugins/{pluginID}/enabled
PUT /v0/management/plugins/{pluginID}/config
PATCH /v0/management/plugins/{pluginID}/config
```

插件自有 Management API 路由通过 `management.register` 的 `routes` 字段注册，并通过 `management.handle` 处理。

可由浏览器直接访问的菜单资源通过 `management.register` 的 `resources` 字段注册。CPA 会将这些资源暴露在 `/v0/resource/plugins/<pluginID>/...` 下；例如插件 ID 为 `example` 且资源路径为 `/status` 时，最终路径是 `/v0/resource/plugins/example/status`。

## 信任边界

标准动态库插件是可信进程内代码。panic 恢复可以保护宿主管理的调用，但不能阻止插件退出进程、破坏内存、修改进程全局状态或泄露敏感数据。只安装你像信任服务二进制一样信任的插件。

## 验证

当前平台示例构建：

```bash
make -C examples/plugin list
make -C examples/plugin build
find examples/plugin/bin -maxdepth 1 -type f | wc -l
make -C examples/plugin clean
```

如果修改了本仓库的 Go 代码，还需要运行：

```bash
go build -o test-output ./cmd/server && rm test-output
```
