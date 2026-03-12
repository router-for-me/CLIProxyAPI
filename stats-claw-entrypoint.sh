#!/bin/sh
set -e

DATA_DIR="/CLIProxyAPI/data"
STATS_FILE="${DATA_DIR}/usage_backup.json"
STATS_R2_DIR="usage"
STATS_R2_KEY="${STATS_R2_DIR}/usage_backup.json"
RCLONE_REMOTE="clawstats"
PORT=8317
MGMT_KEY="${MANAGEMENT_PASSWORD:-}"

# === 1. 生成 config.yaml（主程序启动前）===
if [ -n "$MGMT_KEY" ]; then
  cat > /CLIProxyAPI/config.yaml << EOF
remote-management:
  secret-key: "${MGMT_KEY}"
  allow-remote: false
EOF
  echo "[stats] ✅ config.yaml 已生成"
else
  echo "[stats] ⚠️ MANAGEMENT_PASSWORD 未设置，跳过持久化"
fi

# === 2. rclone 配置（ClawCloud/Sealos 专用）===
setup_rclone() {
  if [ -z "${OBJECTSTORE_ENDPOINT}" ] || [ -z "${OBJECTSTORE_BUCKET}" ] || [ -z "${OBJECTSTORE_ACCESS_KEY}" ] || [ -z "${OBJECTSTORE_SECRET_KEY}" ]; then
    echo "[stats] ?? OBJECTSTORE_* 环境变量未完整配置，跳过"
    return 1
  fi

  mkdir -p /root/.config/rclone
  cat > /root/.config/rclone/rclone.conf << EOF
[$RCLONE_REMOTE]
type = s3
provider = Other
access_key_id = ${OBJECTSTORE_ACCESS_KEY}
secret_access_key = ${OBJECTSTORE_SECRET_KEY}
endpoint = ${OBJECTSTORE_ENDPOINT}
region = auto
force_path_style = true
no_check_bucket = true
EOF

  echo "[stats] √ rclone 已切换为 ClawCloud 兼容模式 (provider=Other + force_path_style=true)"

  # 测试连接（5秒超时）
  echo "[stats] 测试 rclone 连接..."
  if rclone lsd "${RCLONE_REMOTE}:${OBJECTSTORE_BUCKET}" --contimeout 5s --timeout 10s 2>/dev/null; then
    echo "[stats] √ rclone 连接成功"
  else
    echo "[stats] × 首次连接测试失败（bucket 为空正常），继续执行"
  fi
}

# === 3. 下载（目标是目录不是文件）===
download_from_r2() {
  mkdir -p "$DATA_DIR"
  echo "[stats] 下载历史统计..."
  if rclone copy "${RCLONE_REMOTE}:${OBJECTSTORE_BUCKET}/${STATS_R2_KEY}" "${DATA_DIR}/" \
       --contimeout 5s --timeout 15s --retries 1 --s3-no-check-bucket 2>&1; then
    if [ -f "$STATS_FILE" ]; then
      echo "[stats] ✅ 下载成功 ($(wc -c < "$STATS_FILE") bytes)"
    else
      echo "[stats] ℹ️ 远端无历史文件（首次运行正常）"
    fi
  else
    echo "[stats] ⚠️ 下载失败（首次运行正常，继续启动）"
  fi
}

# === 4. 上传 ===
upload_to_r2() {
  if [ -f "$STATS_FILE" ] && [ -s "$STATS_FILE" ]; then
    if rclone copy "$STATS_FILE" "${RCLONE_REMOTE}:${OBJECTSTORE_BUCKET}/${STATS_R2_DIR}/" \
         --contimeout 5s --timeout 15s --retries 2 --s3-no-check-bucket 2>&1; then
      echo "[stats] ✅ 上传成功"
    else
      echo "[stats] ❌ 上传失败"
    fi
  else
    echo "[stats] ℹ️ 无统计文件，跳过上传"
  fi
}

# === 5. 启动主程序 ===
echo "[stats] 启动主服务..."
/CLIProxyAPI/CLIProxyAPI &
SERVER_PID=$!

echo "[stats] 等待主服务就绪（30秒）..."
sleep 30

if [ -n "$MGMT_KEY" ]; then
  setup_rclone && RCLONE_OK=1 || RCLONE_OK=0

  if [ "$RCLONE_OK" = "1" ]; then
    download_from_r2
  fi

  # 导入历史
  if [ -f "$STATS_FILE" ] && [ -s "$STATS_FILE" ]; then
    echo "[stats] 导入历史统计..."
    IMPORT_RESULT=$(curl -s -w "%{http_code}" -X POST \
      -H "X-Management-Key: ${MGMT_KEY}" \
      -H "Content-Type: application/json" \
      -d @"$STATS_FILE" \
      "http://localhost:${PORT}/v0/management/usage/import" 2>/dev/null || echo "000")
    echo "[stats] 导入结果: HTTP ${IMPORT_RESULT}"
  else
    echo "[stats] 首次运行，无历史文件"
  fi

  # === 6. 后台循环 ===
  echo "[stats] ✅ 启动后台循环（每180秒 export+upload）"
  (
    while true; do
      sleep 180
      echo "[stats] $(date '+%H:%M:%S') export..."
      
      RESPONSE=$(curl -s -w "\n%{http_code}" \
        -H "X-Management-Key: ${MGMT_KEY}" \
        "http://localhost:${PORT}/v0/management/usage/export" 2>/dev/null || echo "curl_error\n000")
      
      HTTP_CODE=$(echo "$RESPONSE" | tail -n1)
      BODY=$(echo "$RESPONSE" | sed '$d')

      echo "[stats] Export HTTP: $HTTP_CODE"

      if [ "$HTTP_CODE" = "200" ]; then
        echo "$BODY" > "${STATS_FILE}.tmp" && mv "${STATS_FILE}.tmp" "$STATS_FILE"
        echo "[stats] ✅ Export 成功 ($(wc -c < "$STATS_FILE") bytes)"
        if [ "$RCLONE_OK" = "1" ]; then
          upload_to_r2
        fi
      else
        echo "[stats] ❌ Export 失败 (HTTP $HTTP_CODE)"
        echo "[stats] Body: $(echo "$BODY" | head -c 200)"
      fi
    done
  ) &

fi

# 优雅关闭
trap 'echo "[stats] 关闭中..."; kill -TERM $SERVER_PID 2>/dev/null; wait $SERVER_PID; exit 0' TERM INT
wait $SERVER_PID
