#!/usr/bin/env bash
set -euo pipefail

BASE_DIR="$(cd "$(dirname "$0")" && pwd)"
CONF_FILE="${BASE_DIR}/monitor.conf"

if [[ ! -f "$CONF_FILE" ]]; then
  echo "missing config: ${CONF_FILE}" >&2
  exit 1
fi

# shellcheck disable=SC1090
source "$CONF_FILE"

DOMAIN="${DOMAIN:-cpa.hasdg.fun}"
ORIGIN_IP="${ORIGIN_IP:-}"
API_PATH="${API_PATH:-/v1/models}"
API_KEY="${API_KEY:-}"
API_TIMEOUT="${API_TIMEOUT:-5}"
API_SLOW_MS="${API_SLOW_MS:-2000}"
API_FAIL_THRESHOLD="${API_FAIL_THRESHOLD:-3}"
CPU_WARN="${CPU_WARN:-90}"
MEM_WARN="${MEM_WARN:-90}"
DISK_WARN="${DISK_WARN:-85}"
NET_ERR_INCR="${NET_ERR_INCR:-1}"
CERT_EXPIRE_DAYS="${CERT_EXPIRE_DAYS:-15}"
CONTAINER_NAME="${CONTAINER_NAME:-cliproxyapi}"
RECOVERY_ALERTS="${RECOVERY_ALERTS:-1}"

CHECK_API="${CHECK_API:-1}"
CHECK_DOCKER="${CHECK_DOCKER:-1}"
CHECK_RESOURCE="${CHECK_RESOURCE:-1}"
CHECK_NETWORK="${CHECK_NETWORK:-1}"
CHECK_UFW="${CHECK_UFW:-1}"
CHECK_FAIL2BAN="${CHECK_FAIL2BAN:-1}"
CHECK_CERT="${CHECK_CERT:-1}"
CHECK_ORIGIN="${CHECK_ORIGIN:-1}"

ALERT_COOLDOWN="${ALERT_COOLDOWN:-1800}"
FEISHU_WEBHOOK="${FEISHU_WEBHOOK:-}"

LOG_FILE="${LOG_FILE:-/var/log/cliproxyapi-monitor.log}"
RUN_LOG_FILE="${RUN_LOG_FILE:-/var/log/cliproxyapi-monitor-run.log}"
STATE_DIR="${STATE_DIR:-/var/lib/cliproxyapi-monitor}"
STATE_FILE="${STATE_DIR}/state.env"
ALERT_FILE="${STATE_DIR}/alert.env"
LOCK_FILE="${STATE_DIR}/monitor.lock"

mkdir -p "$STATE_DIR"
touch "$LOG_FILE" "$RUN_LOG_FILE" "$STATE_FILE" "$ALERT_FILE"

exec 9>"$LOCK_FILE"
if ! flock -n 9; then
  echo "$(date '+%Y-%m-%dT%H:%M:%S%z') [INFO] skip overlapped run" >> "$RUN_LOG_FILE"
  exit 0
fi

WARN_COUNT=0
FAIL_COUNT=0
OK_COUNT=0
WARN_ITEMS=()
FAIL_ITEMS=()
OK_ITEMS=()

timestamp() {
  date '+%Y-%m-%dT%H:%M:%S%z'
}

log() {
  local level="$1"; shift
  echo "$(timestamp) [${level}] $*" | tee -a "$LOG_FILE" >> "$RUN_LOG_FILE"
}

get_kv() {
  local file="$1" key="$2"
  grep -E "^${key}=" "$file" | tail -n1 | cut -d= -f2- || true
}

set_kv() {
  local file="$1" key="$2" value="$3"
  if grep -qE "^${key}=" "$file"; then
    sed -i "s#^${key}=.*#${key}=${value}#g" "$file"
  else
    echo "${key}=${value}" >> "$file"
  fi
}

json_escape() {
  printf '%s' "$1" | sed ':a;N;$!ba;s/\n/\\n/g; s/\\/\\\\/g; s/"/\\"/g'
}

record_ok() {
  local item="$1" message="$2"
  OK_COUNT=$((OK_COUNT + 1))
  OK_ITEMS+=("${item}")
  log "INFO" "${item}: ${message}"
}

record_warn() {
  local item="$1" message="$2"
  WARN_COUNT=$((WARN_COUNT + 1))
  WARN_ITEMS+=("${item}")
  log "WARN" "${item}: ${message}"
}

record_fail() {
  local item="$1" message="$2"
  FAIL_COUNT=$((FAIL_COUNT + 1))
  FAIL_ITEMS+=("${item}")
  log "ERROR" "${item}: ${message}"
}

should_alert() {
  local key="$1"
  local now last
  now="$(date +%s)"
  last="$(get_kv "$ALERT_FILE" "${key}_LAST")"
  if [[ -n "$last" && $((now - last)) -lt "$ALERT_COOLDOWN" ]]; then
    return 1
  fi
  set_kv "$ALERT_FILE" "${key}_LAST" "$now"
  return 0
}

mark_alert_active() {
  local key="$1"
  set_kv "$ALERT_FILE" "${key}_ACTIVE" "1"
}

clear_alert_active() {
  local key="$1"
  set_kv "$ALERT_FILE" "${key}_ACTIVE" "0"
}

is_alert_active() {
  local key="$1"
  [[ "$(get_kv "$ALERT_FILE" "${key}_ACTIVE")" == "1" ]]
}

post_feishu() {
  local text="$1"
  if [[ -z "$FEISHU_WEBHOOK" || "$FEISHU_WEBHOOK" == *"REPLACE_ME"* ]]; then
    return 0
  fi
  curl -sS -X POST \
    -H "Content-Type: application/json" \
    -d "{\"msg_type\":\"text\",\"content\":{\"text\":\"$(json_escape "$text")\"}}" \
    "$FEISHU_WEBHOOK" >/dev/null 2>&1 || log "ERROR" "飞书 webhook 发送失败"
}

send_alert() {
  local key="$1" item="$2" message="$3"
  local text="CLIProxyAPI 监控告警
项目: ${item}
域名: ${DOMAIN}
时间: $(date '+%F %T %Z')
详情: ${message}"

  mark_alert_active "$key"

  if ! should_alert "$key"; then
    log "INFO" "alert suppressed: ${key}"
    return 0
  fi

  post_feishu "$text"
}

send_recovery() {
  local key="$1" item="$2" message="$3"
  if [[ "$RECOVERY_ALERTS" -ne 1 ]]; then
    clear_alert_active "$key"
    return 0
  fi
  if ! is_alert_active "$key"; then
    return 0
  fi

  clear_alert_active "$key"
  local text="CLIProxyAPI 监控恢复
项目: ${item}
域名: ${DOMAIN}
时间: $(date '+%F %T %Z')
详情: ${message}"
  log "INFO" "${item}: 已恢复 - ${message}"
  post_feishu "$text"
}

alert_warn() {
  local key="$1" item="$2" message="$3"
  record_warn "$item" "$message"
  send_alert "$key" "$item" "$message"
}

alert_fail() {
  local key="$1" item="$2" message="$3"
  record_fail "$item" "$message"
  send_alert "$key" "$item" "$message"
}

recover_ok() {
  local key="$1" item="$2" message="$3"
  record_ok "$item" "$message"
  send_recovery "$key" "$item" "$message"
}

read_cpu_usage() {
  local line1 line2 idle1 idle2 total1 total2 idle total
  line1=($(grep -E '^cpu ' /proc/stat))
  idle1=$((line1[4] + line1[5]))
  total1=0
  for v in "${line1[@]:1}"; do total1=$((total1 + v)); done

  sleep 1

  line2=($(grep -E '^cpu ' /proc/stat))
  idle2=$((line2[4] + line2[5]))
  total2=0
  for v in "${line2[@]:1}"; do total2=$((total2 + v)); done

  idle=$((idle2 - idle1))
  total=$((total2 - total1))
  if [[ "$total" -le 0 ]]; then
    echo 0
  else
    echo $(( (100 * (total - idle)) / total ))
  fi
}

check_api() {
  local url="https://${DOMAIN}${API_PATH}"
  local header_args=()
  if [[ -n "$API_KEY" ]]; then
    header_args=(-H "Authorization: Bearer ${API_KEY}")
  fi

  local result http_code time_total time_ms fail_count
  result="$(curl -sS -o /dev/null -w "%{http_code} %{time_total}" --connect-timeout "$API_TIMEOUT" "${header_args[@]}" "$url" || echo "000 0")"
  http_code="$(awk '{print $1}' <<< "$result")"
  time_total="$(awk '{print $2}' <<< "$result")"
  time_ms="$(awk -v t="$time_total" 'BEGIN{printf "%d", t*1000}')"

  fail_count="$(get_kv "$STATE_FILE" "API_FAIL_COUNT")"
  fail_count="${fail_count:-0}"

  if [[ "$http_code" != "200" ]]; then
    fail_count=$((fail_count + 1))
    set_kv "$STATE_FILE" "API_FAIL_COUNT" "$fail_count"
    if [[ "$fail_count" -ge "$API_FAIL_THRESHOLD" ]]; then
      alert_fail "api_down" "API" "接口异常，HTTP=${http_code}，连续失败=${fail_count}，URL=${url}"
    else
      record_warn "API" "接口异常但未达到告警阈值，HTTP=${http_code}，连续失败=${fail_count}"
    fi
    send_recovery "api_slow" "API" "响应时间已恢复正常"
    return 0
  fi

  if [[ "$fail_count" -ge "$API_FAIL_THRESHOLD" ]]; then
    send_recovery "api_down" "API" "接口已恢复正常，HTTP=200，URL=${url}"
  fi
  set_kv "$STATE_FILE" "API_FAIL_COUNT" "0"

  if [[ "$time_ms" -ge "$API_SLOW_MS" ]]; then
    alert_warn "api_slow" "API" "响应偏慢，耗时=${time_ms}ms，URL=${url}"
  else
    recover_ok "api_slow" "API" "HTTP=200，耗时=${time_ms}ms"
  fi
}

check_docker() {
  if ! command -v docker >/dev/null 2>&1; then
    alert_fail "docker_missing" "Docker" "docker 命令不存在"
    return 0
  fi
  send_recovery "docker_missing" "Docker" "docker 命令已恢复可用"

  local running restart_count last_restart
  running="$(docker inspect -f '{{.State.Running}}' "$CONTAINER_NAME" 2>/dev/null || echo "false")"
  if [[ "$running" != "true" ]]; then
    alert_fail "container_down" "Docker" "容器未运行：${CONTAINER_NAME}"
    return 0
  fi
  send_recovery "container_down" "Docker" "容器已恢复运行：${CONTAINER_NAME}"

  restart_count="$(docker inspect -f '{{.RestartCount}}' "$CONTAINER_NAME" 2>/dev/null || echo "0")"
  last_restart="$(get_kv "$STATE_FILE" "RESTART_COUNT")"
  last_restart="${last_restart:--1}"

  if [[ "$restart_count" -gt "$last_restart" && "$last_restart" -ge 0 ]]; then
    alert_warn "container_restart" "Docker" "容器重启次数增加：${restart_count}"
  else
    record_ok "Docker" "容器运行中，重启次数=${restart_count}"
  fi

  set_kv "$STATE_FILE" "RESTART_COUNT" "$restart_count"
}

check_resource() {
  local cpu mem disk
  cpu="$(read_cpu_usage)"
  mem="$(awk '/MemTotal/ {t=$2} /MemAvailable/ {a=$2} END {printf "%d", (t-a)*100/t}' /proc/meminfo)"
  disk="$(df -P / | awk 'NR==2 {gsub("%","",$5); print $5}')"

  if [[ "$cpu" -ge "$CPU_WARN" ]]; then
    alert_warn "cpu_high" "CPU" "占用过高：${cpu}%"
  else
    recover_ok "cpu_high" "CPU" "占用=${cpu}%"
  fi

  if [[ "$mem" -ge "$MEM_WARN" ]]; then
    alert_warn "mem_high" "内存" "占用过高：${mem}%"
  else
    recover_ok "mem_high" "内存" "占用=${mem}%"
  fi

  if [[ "$disk" -ge "$DISK_WARN" ]]; then
    alert_warn "disk_high" "磁盘" "根分区占用过高：${disk}%"
  else
    recover_ok "disk_high" "磁盘" "根分区占用=${disk}%"
  fi
}

check_network() {
  local iface rx tx last_rx last_tx
  iface="$(ip route show default 2>/dev/null | awk '{print $5}' | head -n1)"
  if [[ -z "$iface" ]]; then
    record_warn "网络" "未找到默认网卡"
    return 0
  fi

  rx="$(cat /sys/class/net/"$iface"/statistics/rx_errors 2>/dev/null || echo 0)"
  tx="$(cat /sys/class/net/"$iface"/statistics/tx_errors 2>/dev/null || echo 0)"
  last_rx="$(get_kv "$STATE_FILE" "LAST_RX_ERRORS")"; last_rx="${last_rx:-0}"
  last_tx="$(get_kv "$STATE_FILE" "LAST_TX_ERRORS")"; last_tx="${last_tx:-0}"

  if [[ "$rx" -ge $((last_rx + NET_ERR_INCR)) || "$tx" -ge $((last_tx + NET_ERR_INCR)) ]]; then
    alert_warn "net_err" "网络" "错误计数增长：iface=${iface} rx=${rx} tx=${tx}"
  else
    recover_ok "net_err" "网络" "iface=${iface} rx_errors=${rx} tx_errors=${tx}"
  fi

  set_kv "$STATE_FILE" "LAST_RX_ERRORS" "$rx"
  set_kv "$STATE_FILE" "LAST_TX_ERRORS" "$tx"
}

check_ufw() {
  if ! command -v ufw >/dev/null 2>&1; then
    alert_fail "ufw_missing" "UFW" "ufw 未安装"
    return 0
  fi
  send_recovery "ufw_missing" "UFW" "ufw 已恢复可用"

  local status_output
  status_output="$(ufw status 2>/dev/null || true)"
  if ! grep -qi "Status: active" <<< "$status_output"; then
    alert_fail "ufw_inactive" "UFW" "ufw 未启用"
    return 0
  fi
  send_recovery "ufw_inactive" "UFW" "ufw 已恢复启用"

  if grep -E "80/tcp|443/tcp" <<< "$status_output" | grep -E "ALLOW" | grep -E "Anywhere|0.0.0.0/0|::/0" >/dev/null 2>&1; then
    alert_fail "ufw_open" "UFW" "发现 80/443 对全网开放"
  else
    recover_ok "ufw_open" "UFW" "规则启用正常，未发现 80/443 对全网放行"
  fi
}

check_fail2ban() {
  if ! command -v fail2ban-client >/dev/null 2>&1; then
    alert_fail "f2b_missing" "fail2ban" "fail2ban-client 不存在"
    return 0
  fi
  send_recovery "f2b_missing" "fail2ban" "fail2ban-client 已恢复可用"

  if ! systemctl is-active --quiet fail2ban; then
    alert_fail "f2b_inactive" "fail2ban" "服务未运行"
    return 0
  fi
  send_recovery "f2b_inactive" "fail2ban" "服务已恢复运行"

  if ! fail2ban-client status sshd >/dev/null 2>&1; then
    alert_fail "f2b_sshd" "fail2ban" "sshd jail 异常"
  else
    recover_ok "f2b_sshd" "fail2ban" "服务正常，sshd jail 可用"
  fi
}

check_cert() {
  local enddate end_ts now_ts days_left
  enddate="$(echo | openssl s_client -servername "$DOMAIN" -connect "${DOMAIN}:443" 2>/dev/null | openssl x509 -noout -enddate 2>/dev/null | cut -d= -f2-)"
  if [[ -z "$enddate" ]]; then
    alert_fail "cert_read" "证书" "无法读取证书到期时间"
    return 0
  fi
  send_recovery "cert_read" "证书" "证书读取已恢复正常"

  end_ts="$(date -d "$enddate" +%s)"
  now_ts="$(date +%s)"
  days_left=$(( (end_ts - now_ts) / 86400 ))

  if [[ "$days_left" -le "$CERT_EXPIRE_DAYS" ]]; then
    alert_warn "cert_expire" "证书" "证书即将过期，剩余 ${days_left} 天"
  else
    recover_ok "cert_expire" "证书" "剩余 ${days_left} 天"
  fi
}

check_origin() {
  if [[ -z "$ORIGIN_IP" ]]; then
    record_warn "源站回源" "未配置 ORIGIN_IP，跳过检测"
    return 0
  fi

  local url="https://${ORIGIN_IP}${API_PATH}"
  local code
  code="$(curl -k -sS -o /dev/null -w "%{http_code}" --connect-timeout 5 -H "Host: ${DOMAIN}" "$url" || echo "000")"

  if [[ "$code" =~ ^[23] ]]; then
    alert_fail "origin_open" "源站回源" "源站疑似仍可直连，HTTP=${code}，IP=${ORIGIN_IP}"
  else
    recover_ok "origin_open" "源站回源" "直连检测被阻断，HTTP=${code}"
  fi
}

log_summary() {
  local overall="OK"
  if [[ "$FAIL_COUNT" -gt 0 ]]; then
    overall="FAIL"
  elif [[ "$WARN_COUNT" -gt 0 ]]; then
    overall="WARN"
  fi

  log "INFO" "heartbeat: status=${overall} ok=${OK_COUNT} warn=${WARN_COUNT} fail=${FAIL_COUNT}"
  if [[ "${#OK_ITEMS[@]}" -gt 0 ]]; then
    log "INFO" "ok_items: ${OK_ITEMS[*]}"
  fi
  if [[ "${#WARN_ITEMS[@]}" -gt 0 ]]; then
    log "INFO" "warn_items: ${WARN_ITEMS[*]}"
  fi
  if [[ "${#FAIL_ITEMS[@]}" -gt 0 ]]; then
    log "INFO" "fail_items: ${FAIL_ITEMS[*]}"
  fi
  echo "status=${overall} ok=${OK_COUNT} warn=${WARN_COUNT} fail=${FAIL_COUNT}"
}

main() {
  log "INFO" "heartbeat start"
  [[ "$CHECK_API" -eq 1 ]] && check_api || true
  [[ "$CHECK_DOCKER" -eq 1 ]] && check_docker || true
  [[ "$CHECK_RESOURCE" -eq 1 ]] && check_resource || true
  [[ "$CHECK_NETWORK" -eq 1 ]] && check_network || true
  [[ "$CHECK_UFW" -eq 1 ]] && check_ufw || true
  [[ "$CHECK_FAIL2BAN" -eq 1 ]] && check_fail2ban || true
  [[ "$CHECK_CERT" -eq 1 ]] && check_cert || true
  [[ "$CHECK_ORIGIN" -eq 1 ]] && check_origin || true
  log_summary
}

main
