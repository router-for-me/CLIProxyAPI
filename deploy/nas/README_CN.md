# CLIProxyAPI NAS 部署说明

这个仓库原本已经支持 Docker，但默认 `docker-compose.yml` 更偏开发机/通用主机场景。
如果你要部署到群晖、威联通、Unraid、飞牛或 Portainer，建议直接使用新增的 `deploy/nas` 目录。

## 目录说明

- `deploy/nas/docker-compose.yml`：NAS 专用 Compose，默认只暴露 API 端口 `8317`
- `deploy/nas/.env.example`：环境变量样例
- `deploy/nas/config.yaml.example`：最小可改配置

## 绿联 DH4300 Plus 建议

对 `DH4300 Plus`，建议直接在绿联容器应用里创建一个专用共享目录，例如：

- 共享目录名：`docker`
- 应用目录：`docker/cliproxyapi`

然后把下面几个路径都落到这个共享目录里，不要继续使用 `./data/...` 相对路径：

- `CONFIG_FILE=/你的共享目录/docker/cliproxyapi/data/config.yaml`
- `AUTH_DIR=/你的共享目录/docker/cliproxyapi/data/auths`
- `LOG_DIR=/你的共享目录/docker/cliproxyapi/data/logs`
- `STORE_DIR=/你的共享目录/docker/cliproxyapi/data/store`

如果你是在绿联的图形界面里导入 Compose，优先使用“共享文件夹实际挂载路径”或界面里可选的宿主机目录，不要假设系统一定是 `/volume1/...` 这种路径格式。

## 推荐目录结构

建议在 NAS 上准备一个独立目录，例如 `/volume1/docker/cliproxyapi`：

```text
/volume1/docker/cliproxyapi/
├─ docker-compose.yml
├─ .env
└─ data/
   ├─ config.yaml
   ├─ auths/
   ├─ logs/
   └─ store/
```

## 部署步骤

1. 复制部署文件

```bash
mkdir -p /volume1/docker/cliproxyapi/data/auths
mkdir -p /volume1/docker/cliproxyapi/data/logs
mkdir -p /volume1/docker/cliproxyapi/data/store
cp deploy/nas/docker-compose.yml /volume1/docker/cliproxyapi/docker-compose.yml
cp deploy/nas/.env.example /volume1/docker/cliproxyapi/.env
cp deploy/nas/config.yaml.example /volume1/docker/cliproxyapi/data/config.yaml
```

2. 编辑 `.env`

- 如果你用 NAS 的图形化 Docker 管理器，通常要把 `./data/...` 改成绝对路径。
- 在绿联 `DH4300 Plus` 上，优先填你在容器界面里能确认存在的共享目录绝对路径。
- 例如：

```dotenv
CONFIG_FILE=/volume1/docker/cliproxyapi/data/config.yaml
AUTH_DIR=/volume1/docker/cliproxyapi/data/auths
LOG_DIR=/volume1/docker/cliproxyapi/data/logs
STORE_DIR=/volume1/docker/cliproxyapi/data/store
API_PORT=8317
TZ=Asia/Shanghai
```

3. 编辑 `data/config.yaml`

- 至少要改 `api-keys`
- 再把你需要的上游提供商配置，从仓库根目录的 `config.example.yaml` 复制进去
- 如果你要远程访问管理面板，再配置：

```yaml
remote-management:
  allow-remote: true
  secret-key: "change-me"
  disable-control-panel: false
```

4. 启动服务

```bash
cd /volume1/docker/cliproxyapi
docker compose up -d
```

5. 查看日志

```bash
docker compose logs -f
```

## 首次认证建议

NAS 上最容易出问题的是 OAuth 回调端口，不是主 API 端口。

推荐做法：

1. 先在你自己的电脑或一台可交互 Linux 主机上完成登录
2. 把生成出来的认证文件复制到挂载目录 `data/auths/`
3. 重启容器

这样最稳，因为默认 OAuth 登录会使用这些本地回调端口：

- Gemini: `8085`
- Codex: `1455`
- Claude: `54545`
- Antigravity: `51121`
- iFlow: `11451`

如果你一定要在容器里完成 OAuth：

- 临时取消 `deploy/nas/docker-compose.yml` 里对应回调端口的注释
- 确保浏览器能访问 `http://NAS_IP:对应端口/...`
- 完成认证后，可以再把这些端口关闭

## 验证接口

把 `your-api-key` 替换成你在 `config.yaml` 里设置的 key：

```bash
curl http://NAS_IP:8317/v1/models \
  -H "Authorization: Bearer your-api-key"
```

如果返回模型列表或空数组但不是 `401`/`403`，说明容器已经正常对外服务。

## 反向代理建议

- 对外只转发 `8317`
- 管理接口尽量不要直接公网开放
- 如果必须远程使用管理面板，至少同时满足：
  - `remote-management.allow-remote: true`
  - 设置强密码 `remote-management.secret-key` 或环境变量 `MANAGEMENT_PASSWORD`
  - 通过内网、VPN 或反向代理白名单限制访问

## 升级

```bash
cd /volume1/docker/cliproxyapi
docker compose pull
docker compose up -d
```

## 回滚

最简单的方式是把镜像 tag 固定成某个版本，例如：

```dotenv
CLI_PROXY_IMAGE=eceasy/cli-proxy-api:v6.3.0
```

然后重新执行：

```bash
docker compose up -d
```
