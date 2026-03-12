#!/bin/sh
set -e

STATS_FILE="/CLIProxyAPI/data/usage_backup.json"
STATS_R2_KEY="usage/usage_backup.json"
RCLONE_REMOTE="clawstats"
PORT=8317
MGMT_KEY="${MANAGEMENT_PASSWORD:-}"

# === 1. 立即生成 config.yaml（必须在主程序启动前！）===
if [ -n "$MGMT_KEY" ]; then
  mkdir -p /CLIProxyAPI
  cat > /CLIProxyAPI/config.yaml << EOF
remote-management:
  secret-key: "${MGMT_KEY}"
  allow-remote: false
EOF
  echo "[stats] ✅ 已自动生成 config.yaml（management key 已注入，主程序启动时即可使用）"
else
  echo "[stats] ⚠️ MANAGEMENT_PASSWORD 未设置，跳过 stats 持久化"
fi

setup_rclone() {
  if [ -z "${OBJECTSTORE_ENDPOINT}" ] || [ -z "${OBJECTSTORE_BUCKET}" ]; then
    echo "[stats] OBJECTSTORE 未配置，跳过 ClawCloud。"
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
  else
    echo "[stats] 无有效统计文件，跳过上传。"
  fi
}

# === 2. 启动主程序（现在 config 已存在！）===
echo "[stats] 启动主服务..."
/CLIProxyAPI/CLIProxyAPI &
SERVER_PID=$!

echo "[stats] 主服务启动，等待就绪（30秒防初始化）..."
sleep 30

if [ -n "$MGMT_KEY" ]; then
  setup_rclone
  download_from_r2

  echo "[stats] 尝试导入历史统计..."
  if [ -f "$STATS_FILE" ]; then
    curl -s -X POST -H "X-Management-Key: ${MGMT_KEY}" -H "Content-Type: application/json" -d @"$STATS_FILE" "http://localhost:${PORT}/v0/management/usage/import" || echo "[stats] 导入失败（可能是首次运行，无历史文件）"
  else
    echo "[stats] 首次运行，无历史文件，跳过导入。"
  fi

  echo "[stats] 启动后台每 60 秒上传循环（调试模式，成功后改回 300）..."
  while true; do
    echo "[stats debug] $(date '+%H:%M:%S') 开始 export..."
    RESPONSE=$(curl -s -w "\n%{http_code}" -H "X-Management-Key: ${MGMT_KEY}" "http://localhost:${PORT}/v0/management/usage/export" 2>/dev/null || echo "curl_failed\n999")
    HTTP_CODE=$(echo "$RESPONSE" | tail -n1)
    BODY=$(echo "$RESPONSE" | sed '$d')
    echo "[stats debug] Export HTTP code: $HTTP_CODE"

    if [ "$HTTP_CODE" = "200" ]; then
      echo "$BODY" > "${STATS_FILE}.tmp" && mv "${STATS_FILE}.tmp" "$STATS_FILE"
      upload_to_r2
    else
      echo "[stats] ❌ Export 失败 (code: $HTTP_CODE)，常见原因已排除（config 已提前注入）"
      if [ -n "$BODY" ]; then echo "[stats debug] Response preview: ${BODY:0:200}"; fi
    fi
    sleep 60
  done &
fi

# 优雅关闭
trap 'echo "[stats] 优雅关闭..."; if [ -n "$MGMT_KEY" ]; then curl -s -H "X-Management-Key: ${MGMT_KEY}" "http://localhost:${PORT}/v0/management/usage/export" > "$STATS_FILE.tmp" && mv "$STATS_FILE.tmp" "$STATS_FILE" && upload_to_r2; fi; kill -TERM $SERVER_PID 2>/dev/null; wait $SERVER_PID; exit 0' TERM INT
wait $SERVER_PID
