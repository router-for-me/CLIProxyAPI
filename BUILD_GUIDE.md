# CLIProxyAPI Build Scripts

## build-optimized.ps1

可复用的 PowerShell 构建脚本，支持多种构建配置。

### 快速开始

```powershell
# 基础构建
.\build-optimized.ps1

# 优化构建（推荐用于发布）
.\build-optimized.ps1 -Optimized

# 优化构建 + UPX 压缩
.\build-optimized.ps1 -Optimized -WithUPX
```

### 参数说明

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `-OutputName` | string | `cli-proxy` | 输出文件名（不含扩展名） |
| `-GoModulePath` | string | `./cmd/server` | Go 模块入口路径 |
| `-Optimized` | switch | false | 启用优化构建模式 |
| `-WithUPX` | switch | false | 使用 UPX 压缩（需安装 UPX） |
| `-SkipVersionGit` | switch | false | 跳过从 Git 获取版本信息 |
| `-CustomVersion` | string | - | 指定自定义版本号 |
| `-CustomCommit` | string | - | 指定自定义提交哈希 |
| `-Help` | switch | false | 显示帮助信息 |

### 环境变量

| 变量 | 说明 | 优先级 |
|------|------|--------|
| `VERSION` | 版本号 | 最高 |
| `COMMIT` | 提交哈希 | 最高 |
| `BUILDDATE` | 构建日期（格式：yyyy-MM-ddTHH:mm:ssZ） | - |
| `CGO_ENABLED` | CGO 设置 | - |

### 优化模式功能

当使用 `-Optimized` 参数时：

1. **禁用 CGO**：设置 `CGO_ENABLED=0`
2. **剥离调试符号**：使用 `-s -w` 标志
3. **移除构建 ID**：使用 `-buildid=`
4. **路径裁剪**：使用 `-trimpath` 标志

### UPX 压缩

使用 `-WithUPX` 参数需要提前安装 UPX：

```powershell
# 使用 Chocolatey 安装
choco install upx

# 或从 https://upx.github.io 下载
```

### 示例

```powershell
# 自定义版本和提交
.\build-optimized.ps1 -CustomVersion "v1.0.0" -CustomCommit "abc1234"

# 使用环境变量
$env:VERSION = "v2.0.0"
$env:COMMIT = "def5678"
.\build-optimized.ps1

# 跳过 Git 信息
.\build-optimized.ps1 -SkipVersionGit -CustomVersion "local-build"

# 自定义输出和路径
.\build-optimized.ps1 -OutputName "myapp" -GoModulePath "./cmd/myapp" -Optimized
```

### 构建产物

构建完成后会在脚本所在目录生成：
- `cli-proxy.exe` - 可执行文件

### 文件大小对比

典型文件大小示例（CLIProxyAPI 项目）：

| 构建模式 | 文件大小 | 节省比例 |
|----------|----------|----------|
| 基础构建 | ~52 MB | - |
| 优化构建 | ~40 MB | ~22% |
| 优化+UPX | ~15 MB | ~70% |

### 注意事项

1. **Git 信息**：脚本会自动从 `.git` 目录读取版本和提交信息
2. **版本验证**：构建后会尝试运行 `--version` 或 `--help` 验证版本信息
3. **错误处理**：使用 `$ErrorActionPreference = "Stop"` 确保错误时立即停止
4. **UPX 兼容性**：部分杀毒软件可能对 UPX 压缩的文件报警

### 故障排除

**问题**：UPX 压缩失败
**解决**：确保 UPX 已安装并添加到 PATH

**问题**：Git 信息获取失败
**解决**：使用 `-SkipVersionGit` 参数跳过，或设置 `VERSION` 和 `COMMIT` 环境变量

**问题**：构建成功但版本信息不正确
**解决**：检查 `main.Version`、`main.Commit`、`main.BuildDate` 变量是否正确定义
