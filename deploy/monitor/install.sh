#!/usr/bin/env bash
set -euo pipefail

if [[ "${EUID}" -ne 0 ]]; then
  echo "请使用 root 执行"
  exit 1
fi

SRC_DIR="$(cd "$(dirname "$0")" && pwd)"
TARGET_DIR="/opt/CLIProxyAPI/monitor"
CONF_TARGET="${TARGET_DIR}/monitor.conf"
CRON_FILE="/etc/cron.d/cliproxyapi-monitor"
LOGROTATE_FILE="/etc/logrotate.d/cliproxyapi-monitor"

mkdir -p "$TARGET_DIR"
cp -f "${SRC_DIR}/monitor.sh" "${TARGET_DIR}/monitor.sh"
chmod +x "${TARGET_DIR}/monitor.sh"

if [[ ! -f "$CONF_TARGET" ]]; then
  cp -f "${SRC_DIR}/monitor.conf.example" "$CONF_TARGET"
  echo "已生成配置: ${CONF_TARGET}"
fi

mkdir -p /var/lib/cliproxyapi-monitor
touch /var/log/cliproxyapi-monitor.log
touch /var/log/cliproxyapi-monitor-run.log

cat > "$CRON_FILE" <<'EOF'
* * * * * root /opt/CLIProxyAPI/monitor/monitor.sh >> /var/log/cliproxyapi-monitor-run.log 2>&1
EOF

cat > "$LOGROTATE_FILE" <<'EOF'
/var/log/cliproxyapi-monitor.log /var/log/cliproxyapi-monitor-run.log {
    su root root
    daily
    rotate 14
    compress
    delaycompress
    missingok
    notifempty
    copytruncate
    create 0640 root root
}
EOF

echo "已安装定时任务: ${CRON_FILE}"
echo "已安装日志轮转: ${LOGROTATE_FILE}"
echo "cron 输出已写入: /var/log/cliproxyapi-monitor-run.log"
echo "请编辑配置并手动测试："
echo "  vi ${CONF_TARGET}"
echo "  /opt/CLIProxyAPI/monitor/monitor.sh"
