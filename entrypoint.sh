#!/bin/sh
set -e

CONFIG_FILE="/CLIProxyAPI/config/config.yaml"
STATS_FILE="/CLIProxyAPI/data/usage_backup.json"

# ====== 1. 初始化配置 ======
if [ ! -f "$CONFIG_FILE" ]; then
  cp /tmp/config_init.yaml "$CONFIG_FILE"
fi

# ====== 2. 从配置文件中提取端口和管理密钥 ======
# === 从环境变量获取管理密钥 ===
get_management_key() {
    echo "${MANAGEMENT_PASSWORD:-}"
}
get_port() {
  PORT=$(grep -E "^port:" "$CONFIG_FILE" 2>/dev/null | sed -E 's/^port: *["'"'"']?([0-9]+)["'"'"']?.*$/\1/')
  echo "${PORT:-8317}"
}

# ====== 3. 等待服务就绪 ======
wait_for_service() {
  local port=$1
  local max_retries=30
  local retry=0
  while [ $retry -lt $max_retries ]; do
    if curl -s -o /dev/null -w "%{http_code}" "http://localhost:${port}/" 2>/dev/null | grep -q "200"; then
      return 0
    fi
    retry=$((retry + 1))
    sleep 1
  done
  return 1
}

# ====== 4. 导入历史统计数据 ======
import_stats() {
  local port=$1
  local mgmt_key=$2
  if [ ! -f "$STATS_FILE" ]; then
    echo "[entrypoint] No usage backup found, skipping import."
    return
  fi
  echo "[entrypoint] Importing usage statistics from backup..."
  RESPONSE=$(curl -s -w "\n%{http_code}" -X POST \
    -H "X-Management-Key: ${mgmt_key}" \
    -H "Content-Type: application/json" \
    -d @"$STATS_FILE" \
    "http://localhost:${port}/v0/management/usage/import" 2>/dev/null)
  HTTP_CODE=$(echo "$RESPONSE" | tail -n1)
  if [ "$HTTP_CODE" = "200" ]; then
    echo "[entrypoint] Usage statistics imported successfully."
  else
    echo "[entrypoint] Warning: Import failed (HTTP $HTTP_CODE)"
  fi
}

# ====== 5. 定时导出统计数据（后台进程） ======
periodic_export() {
  local port=$1
  local mgmt_key=$2
  local interval=${USAGE_BACKUP_INTERVAL:-300}  # 默认5分钟
  while true; do
    sleep "$interval"
    RESPONSE=$(curl -s -w "\n%{http_code}" \
      -H "X-Management-Key: ${mgmt_key}" \
      "http://localhost:${port}/v0/management/usage/export" 2>/dev/null)
    HTTP_CODE=$(echo "$RESPONSE" | tail -n1)
    BODY=$(echo "$RESPONSE" | sed '$d')
    if [ "$HTTP_CODE" = "200" ]; then
      echo "$BODY" > "${STATS_FILE}.tmp" && mv "${STATS_FILE}.tmp" "$STATS_FILE"
    fi
  done
}

# ====== 6. 优雅关闭：收到信号时先导出再退出 ======
graceful_shutdown() {
  echo "[entrypoint] Received shutdown signal, exporting statistics..."
  PORT=$(get_port)
  MGMT_KEY=$(get_management_key)
  if [ -n "$MGMT_KEY" ]; then
    RESPONSE=$(curl -s -w "\n%{http_code}" \
      -H "X-Management-Key: ${MGMT_KEY}" \
      "http://localhost:${PORT}/v0/management/usage/export" 2>/dev/null)
    HTTP_CODE=$(echo "$RESPONSE" | tail -n1)
    BODY=$(echo "$RESPONSE" | sed '$d')
    if [ "$HTTP_CODE" = "200" ]; then
      echo "$BODY" > "${STATS_FILE}.tmp" && mv "${STATS_FILE}.tmp" "$STATS_FILE"
      echo "[entrypoint] Final export completed."
    fi
  fi
  # 转发信号给主进程
  if [ -n "$SERVER_PID" ]; then
    kill -TERM "$SERVER_PID" 2>/dev/null
    wait "$SERVER_PID" 2>/dev/null
  fi
  exit 0
}

# 注册信号处理
trap graceful_shutdown TERM INT

# ====== 7. 启动主服务（后台运行） ======
./CLIProxyAPI -config "$CONFIG_FILE" &
SERVER_PID=$!

# ====== 8. 等待服务就绪后执行导入和定时备份 ======
PORT=$(get_port)
MGMT_KEY=$(get_management_key)

if wait_for_service "$PORT"; then
  if [ -n "$MGMT_KEY" ]; then
    import_stats "$PORT" "$MGMT_KEY"
    periodic_export "$PORT" "$MGMT_KEY" &
    EXPORT_PID=$!
  else
    echo "[entrypoint] Warning: No management-api-key found, usage backup disabled."
  fi
else
  echo "[entrypoint] Warning: Service did not become ready, skipping usage import."
fi

# 等待主进程
wait "$SERVER_PID"
