# 部署到 Linux 服务器

以 ice-server 为例（`103.91.219.4`，SSH 端口 `22122`，用户 `iec`）。

## 1. 编译 Linux 二进制

在 Mac 本地交叉编译：

```bash
cd ~/workspace/CLIProxyAPI
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o CLIProxyAPI-linux ./cmd/server/
```

## 2. 准备服务器目录

```bash
ssh -p 22122 iec@103.91.219.4 "mkdir -p /home/iec/cliproxyapi/auths"
```

## 3. 传输文件

```bash
scp -P 22122 CLIProxyAPI-linux          iec@103.91.219.4:/home/iec/cliproxyapi/
scp -P 22122 config.yaml                iec@103.91.219.4:/home/iec/cliproxyapi/
scp -P 22122 auths/codex-*.json         iec@103.91.219.4:/home/iec/cliproxyapi/auths/
```

## 4. 调整 config.yaml

服务器上的配置与本地有几处不同：

| 字段 | 本地值 | 服务器值 |
|------|--------|----------|
| `host` | `"127.0.0.1"` | `""` （绑定所有网卡） |
| `auth-dir` | `"./auths"` | `"/home/iec/cliproxyapi/auths"` |
| `debug` | `true` | `false` |
| `pprof.enable` | `true` | `false` |

## 5. 用 systemd 管理进程

创建 service 文件：

```bash
sudo tee /etc/systemd/system/cliproxyapi.service > /dev/null <<EOF
[Unit]
Description=CLIProxyAPI
After=network.target

[Service]
Type=simple
User=iec
WorkingDirectory=/home/iec/cliproxyapi
ExecStart=/home/iec/cliproxyapi/CLIProxyAPI-linux -config /home/iec/cliproxyapi/config.yaml
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
```

启动并设置开机自启：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now cliproxyapi
sudo systemctl status cliproxyapi
```

常用管理命令：

```bash
sudo systemctl start cliproxyapi
sudo systemctl stop cliproxyapi
sudo systemctl restart cliproxyapi
journalctl -u cliproxyapi -f   # 实时查看日志
```

## 6. 开放防火墙端口

```bash
sudo ufw allow 8317/tcp
```

或在阿里云控制台安全组添加入站规则开放 `8317` 端口。

## 更新部署

每次更新只需重新编译并替换二进制：

```bash
# 本地
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o CLIProxyAPI-linux ./cmd/server/
scp -P 22122 CLIProxyAPI-linux iec@103.91.219.4:/home/iec/cliproxyapi/

# 服务器
ssh -p 22122 iec@103.91.219.4 "sudo systemctl restart cliproxyapi"
```
