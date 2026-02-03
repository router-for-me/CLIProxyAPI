# 废弃文件说明

## 废弃的部署文件

以下文件已被统一的云平台部署方案替代，保留仅为兼容性考虑。

### 废弃文件列表

| 文件 | 替代方案 | 废弃原因 |
|------|---------|---------|
| `Dockerfile.railway` | `Dockerfile` | 统一使用主 Dockerfile |
| `start-railway.sh` | `start-cloud.sh` | 统一使用通用云平台启动脚本 |
| `start-zeabur.sh` | `start-cloud.sh` | 统一使用通用云平台启动脚本 |
| `config.railway.yaml` | 环境变量配置 | 改用 `start-cloud.sh` 动态生成配置 |

### 当前部署方案

所有云平台部署（Railway、Zeabur 等）现已统一使用：

- **Dockerfile**: 主 Dockerfile（支持多平台）
- **启动脚本**: `start-cloud.sh`（通用云平台启动脚本）
- **配置方式**: 通过环境变量动态生成 `config.yaml`

### Railway 部署

**配置文件**: `railway.toml`

```toml
[build]
builder = "dockerfile"
dockerfilePath = "./Dockerfile"
```

**环境变量**: 见 [RAILWAY_DEPLOYMENT.md](RAILWAY_DEPLOYMENT.md)

### Zeabur 部署

**配置文件**: `Dockerfile.zeabur`（指向 `start-cloud.sh`）

**环境变量**: 与 Railway 相同，见 [RAILWAY_DEPLOYMENT.md](RAILWAY_DEPLOYMENT.md)

## 迁移指南

如果你之前使用的是旧的部署文件：

1. **无需更改代码**：现有部署会自动使用新的统一方案
2. **环境变量保持不变**：所有环境变量配置保持兼容
3. **重新部署**：下次部署时会自动使用新的 Dockerfile 和启动脚本

## 未来计划

这些废弃文件将在下一个主要版本中移除（v2.0.0）。

如有问题，请参考：
- [RAILWAY_DEPLOYMENT.md](RAILWAY_DEPLOYMENT.md) - Railway 部署完整指南
- [README.md](README.md) - 项目主文档
