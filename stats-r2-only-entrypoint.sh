#!/bin/sh
set -e

STATS_FILE="/CLIProxyAPI/data/usage_backup.json"
STATS_R2_KEY="usage/usage_backup.json"
RCLONE_REMOTE="clawstats"
PORT=8317
MGMT_KEY="${MANAGEMENT_PASSWORD:-}"

setup_rclone() {
  if [ -z "${OBJECTSTORE_ENDPOINT}" ] || [ -z "${OBJECTSTORE_BUCKET}" ]; then
    echo "[stats] OBJECTSTORE 未配置，跳过 R2/ClawCloud 持久化。"
    return 1
  fi
  mkdir -p /root/.config/rclone
  cat > /root/.config/rclone/rclone.conf << EOF
[$RCLONE_REMOTE]
type = s3
provider = AWS
access_key_id = ${OBJECTSTORE_ACCESS_KEY}
secret_access_key = ${OBJECTSTORE_SECRET_KEY}
endpoint = ${OBJECTSTORE_ENDPOINT}
region = auto
no_check_bucket = true
EOF
  echo "[stats] rclone ClawCloud 配置完成。"
}

download_from_r2() {
  echo "[stats] 从 ClawCloud 下载历史统计..."
  rclone copy "${RCLONE_REMOTE}:${OBJECTSTORE_BUCKET}/${STATS_R2_KEY}" "$STATS_FILE" --s3-no-check-bucket 2>/dev/null || true
}

upload_to_r2() {
  if [ -f "$STATS_FILE" ] && [ -s "$STATS_FILE" ]; then
    echo "[stats] 上传统计到 ClawCloud..."
    rclone copy "$STATS_FILE" "${RCLONE_REMOTE}:${OBJECTSTORE_BUCKET}/${STATS_R2_KEY}" --s3-no-check-bucket
  fi
}

# ====== 启动官方主程序 ======
/CLIProxyAPI/CLIProxyAPI &
SERVER_PID=$!

# ====== 等待 + 导入统计 ======
sleep 5
if [ -n "$MGMT_KEY" ]; then
  setup_rclone
  download_from_r2
  if [ -f "$STATS_FILE" ]; then
    echo "[stats] 导入历史统计..."
    curl -s -X POST \
      -H "X-Management-Key: ${MGMT_KEY}" \
      -H "Content-Type: application/json" \
      -d @"$STATS_FILE" \
      "http://localhost:${PORT}/v0/management/usage/import" || true
  fi

  # ====== 每 5 分钟备份 ======
  while true; do
    sleep 300
    RESPONSE=$(curl -s -w "\n%{http_code}" -H "X-Management-Key: ${MGMT_KEY}" "http://localhost:${PORT}/v0/management/usage/export" 2>/dev/null)
    HTTP_CODE=$(echo "$RESPONSE" | tail -n1)
    BODY=$(echo "$RESPONSE" | sed '$d')
    if [ "$HTTP_CODE" = "200" ]; then
      echo "$BODY" > "${STATS_FILE}.tmp" && mv "${STATS_FILE}.tmp" "$STATS_FILE"
      upload_to_r2
    fi
  done &
fi

# ====== 优雅关闭 ======
trap 'echo "[stats] 优雅关闭，导出并上传..."; if [ -n "$MGMT_KEY" ]; then curl -s -H "X-Management-Key: ${MGMT_KEY}" "http://localhost:${PORT}/v0/management/usage/export" > "$STATS_FILE.tmp" && mv "$STATS_FILE.tmp" "$STATS_FILE" && upload_to_r2; fi; kill -TERM $SERVER_PID 2>/dev/null; wait $SERVER_PID; exit 0' TERM INT

wait $SERVER_PID
