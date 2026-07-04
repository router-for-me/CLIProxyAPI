# CLIProxyAPI(指纹加固 fork)服务器部署

本发布包为 **Linux 动态链接二进制**(`CGO_ENABLED=1` 编译,复用仓库根 `Dockerfile` 的
`golang:1.26-bookworm` builder,glibc 基线 = debian bookworm **2.36**)。**支持 Go 原生
`.so` 插件**——插件宿主依赖 CGO,纯静态(CGO=0)会静默禁用整个插件系统 + 前端插件市场。
- 裸机 systemd 部署需目标机 **glibc ≥ 2.36**:Debian 12+ / Ubuntu 24.04+ / Rocky 9+ / Alma 9+ 满足;
  ⚠️ **Alpine/musl 不兼容**,老发行版(CentOS 7 = glibc 2.17)也**不兼容**(会报 `GLIBC_2.xx not found`)。
- 目标机 glibc 过旧就改用 **GHCR 镜像**(`ghcr.io/<owner>/cliproxyapi`,自带 debian:bookworm runtime,
  与本二进制同源同 glibc),用 docker/compose 部署。

包内容:
- `cli-proxy-api` — 主程序(CGO=1 动态链接,支持 `.so` 插件)
- `config.example.yaml` — 配置模板
- `cli-proxy-api.service` — systemd 单元
- `install.sh` — 一键安装脚本
- `DEPLOY.md` — 本文件

## 一键安装(推荐)

```bash
tar -xzf CLIProxyAPI_<版本>_linux_amd64.tar.gz
cd CLIProxyAPI_<版本>_linux_amd64
sudo ./install.sh
sudo nano /opt/cli-proxy-api/config.yaml   # 填入认证/密钥
sudo systemctl start cli-proxy-api
systemctl status cli-proxy-api
```

安装脚本会:创建系统用户 `cliproxy` → 安装到 `/opt/cli-proxy-api` →
首次生成 `config.yaml`(升级不覆盖)→ 注册并 enable systemd 服务。

## 手动安装

```bash
sudo useradd --system --home-dir /opt/cli-proxy-api --shell /usr/sbin/nologin cliproxy
sudo mkdir -p /opt/cli-proxy-api
sudo cp cli-proxy-api config.example.yaml /opt/cli-proxy-api/
sudo cp config.example.yaml /opt/cli-proxy-api/config.yaml
sudo cp cli-proxy-api.service /etc/systemd/system/
sudo chown -R cliproxy:cliproxy /opt/cli-proxy-api
sudo systemctl daemon-reload
sudo systemctl enable --now cli-proxy-api
```

## 配置要点(`/opt/cli-proxy-api/config.yaml`)

- `port: 8317` — 监听端口。
- `auth-dir` — 账号凭证目录,默认 `~/.cli-proxy-api`;systemd 下 `HOME=/opt/cli-proxy-api`,
  即 `/opt/cli-proxy-api/.cli-proxy-api`。也可写绝对路径。
- `remote-management` — 远程管理接口,**务必设强密码**再对外暴露。
- `proxy-url` — 如需出站走代理(socks5/http)在此配置。
- **指纹加固开关**(本 fork 特有,默认全开=生效,详见仓库 `docs/fingerprint-hardening.md`):
  `disable-dateline-normalization` / `disable-node-tls-fingerprint` /
  `disable-upstream-cookie-jar` / `disable-fingerprint-randomization`——正常保持默认即可。

## 登录上游账号(OAuth)

服务器通常无浏览器,用 `-no-browser`,程序会打印授权链接,你在本地浏览器打开完成:

```bash
sudo -u cliproxy HOME=/opt/cli-proxy-api /opt/cli-proxy-api/cli-proxy-api \
  -claude-login -no-browser -config /opt/cli-proxy-api/config.yaml
```

其他:`-codex-login`、`-codex-device-login`、`-antigravity-login`、`-kimi-login`、`-xai-login`。
凭证写入 `auth-dir`,重启保留。

## 升级

```bash
sudo systemctl stop cli-proxy-api
# 解压新版本包后:
sudo install -m 0755 cli-proxy-api /opt/cli-proxy-api/cli-proxy-api
sudo systemctl start cli-proxy-api
```

`config.yaml` 与 `auth-dir` 不受影响。也可直接重跑 `sudo ./install.sh`(不覆盖 config)。

## 卸载

```bash
sudo systemctl disable --now cli-proxy-api
sudo rm /etc/systemd/system/cli-proxy-api.service
sudo systemctl daemon-reload
sudo rm -rf /opt/cli-proxy-api          # 会删除凭证,谨慎
sudo userdel cliproxy
```

## 反向代理(可选)

对外建议用 Nginx/Caddy 加 TLS,反代到 `127.0.0.1:8317`。示例(Caddy):

```
api.example.com {
    reverse_proxy 127.0.0.1:8317
}
```

## 验证运行

```bash
curl -sS http://127.0.0.1:8317/health || curl -sS http://127.0.0.1:8317/
journalctl -u cli-proxy-api -f
```
