#!/usr/bin/env bash
# CLIProxyAPI(指纹加固 fork)一键安装到 systemd。
# 用法:解压发布包后进入目录, sudo ./install.sh
set -euo pipefail

APP=cli-proxy-api
PREFIX=/opt/cli-proxy-api
RUN_USER=cliproxy
SERVICE=/etc/systemd/system/${APP}.service

[ "$(id -u)" = 0 ] || { echo "请用 root 运行:  sudo ./install.sh"; exit 1; }
SRC="$(cd "$(dirname "$0")" && pwd)"

echo "==> 创建运行用户 ${RUN_USER}(若不存在)"
id -u "$RUN_USER" >/dev/null 2>&1 || useradd --system --home-dir "$PREFIX" --shell /usr/sbin/nologin "$RUN_USER"

echo "==> 安装二进制到 ${PREFIX}"
mkdir -p "$PREFIX"
install -m 0755 "$SRC/$APP" "$PREFIX/$APP"
cp -f "$SRC/config.example.yaml" "$PREFIX/config.example.yaml"

if [ ! -f "$PREFIX/config.yaml" ]; then
  cp "$SRC/config.example.yaml" "$PREFIX/config.yaml"
  echo "==> 已生成 $PREFIX/config.yaml(请编辑填入认证/密钥)"
else
  echo "==> 保留已存在的 $PREFIX/config.yaml(升级不覆盖)"
fi

chown -R "$RUN_USER:$RUN_USER" "$PREFIX"

echo "==> 安装 systemd 服务"
install -m 0644 "$SRC/${APP}.service" "$SERVICE"
systemctl daemon-reload
systemctl enable "$APP" >/dev/null 2>&1 || true

cat <<EOF

安装完成。下一步:
  1) 编辑配置:      sudo nano $PREFIX/config.yaml
  2) (可选)登录上游账号,如 Claude(服务器无浏览器时用 -no-browser,按提示打开链接):
       sudo -u $RUN_USER HOME=$PREFIX $PREFIX/$APP -claude-login -no-browser -config $PREFIX/config.yaml
     其他:-codex-login / -antigravity-login / -kimi-login / -xai-login
  3) 启动:          sudo systemctl start $APP
  4) 状态与日志:    systemctl status $APP    journalctl -u $APP -f

  默认监听端口 8317(见 config.yaml 的 port)。
EOF
