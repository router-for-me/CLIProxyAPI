#!/usr/bin/env bash
# Verification harness for CLIProxyAPI. Subcommands: launch | doctor | drive <feature> | cleanup | all
# State dir (instance, never evidence): $CPA_RUN (default /tmp/cpa-verify-run)
# Evidence dir (survives cleanup):      $CPA_EVIDENCE (default /tmp/cpa-verify-evidence/<run-id>)
set -euo pipefail
REPO="$(cd "$(dirname "$0")/../../../.." && pwd)"
RUN="${CPA_RUN:-/tmp/cpa-verify-run}"
st() { cat "$RUN/$1" 2>/dev/null || true; }
die() { echo "FAIL: $*" >&2; exit 1; }

launch() {
  [ -f "$RUN/pid" ] && kill -0 "$(st pid)" 2>/dev/null && die "instance already running (pid $(st pid)); run cleanup first"
  rm -rf "$RUN"; mkdir -p "$RUN/auths" "$RUN/logs"
  local port; port="$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])')"
  local apikey="verify-$(openssl rand -hex 8)" mgmt="mgmt-$(openssl rand -hex 8)"
  local ev="${CPA_EVIDENCE:-/tmp/cpa-verify-evidence/$(date +%Y%m%d-%H%M%S)-$$}"
  mkdir -p "$ev"
  echo "$port" >"$RUN/port"; echo "$apikey" >"$RUN/apikey"; echo "$mgmt" >"$RUN/mgmtkey"; echo "$ev" >"$RUN/evidence"
  chmod 600 "$RUN/apikey" "$RUN/mgmtkey"
  cat >"$RUN/config.yaml" <<YAML
host: "127.0.0.1"
port: $port
remote-management:
  allow-remote: false
  secret-key: "$mgmt"
  disable-control-panel: true
auth-dir: "$RUN/auths"
api-keys:
  - "$apikey"
debug: false
YAML
  (cd "$REPO" && go build -o "$RUN/cli-proxy-api" ./cmd/server) || die "go build failed"
  (cd "$RUN" && git -C "$REPO" rev-parse --short HEAD >"$RUN/rev")
  cd "$RUN"; nohup "$RUN/cli-proxy-api" -config "$RUN/config.yaml" -local-model >"$RUN/server.log" 2>&1 </dev/null &
  echo $! >"$RUN/pid"; cd - >/dev/null
  for _ in $(seq 1 60); do
    curl -fsS "http://127.0.0.1:$port/healthz" >/dev/null 2>&1 && { echo "READY pid=$(st pid) port=$port evidence=$ev"; return 0; }
    kill -0 "$(st pid)" 2>/dev/null || { tail -20 "$RUN/server.log"; die "server exited"; }
    sleep 0.5
  done
  die "not ready after 30s (see $RUN/server.log)"
}

doctor() {
  local pid port; pid="$(st pid)"; port="$(st port)"
  [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null || die "no live instance started by this harness"
  ss -ltnp 2>/dev/null | grep -q ":$port .*pid=$pid," || die "port $port is not owned by pid $pid"
  curl -fsS "http://127.0.0.1:$port/healthz" >/dev/null || die "/healthz not OK"
  local code; code="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $(st mgmtkey)" "http://127.0.0.1:$port/v0/management/config")"
  [ "$code" = 200 ] || die "management key rejected ($code)"
  echo "OK pid=$pid port=$port rev=$(st rev) evidence=$(st evidence)"
}

# req <name> <curl args...> : records request line, status, headers, body under evidence/<feature>/
req() {
  local name="$1"; shift; local d="$EVD/$name"
  printf '%s\n' "curl $*" | sed -E 's/(Bearer |x-api-key: |X-Management-Key: )[^ "]+/\1<redacted>/g' >"$d.cmd"
  curl -sS -D "$d.headers" -o "$d.body" -w '%{http_code}' "$@" >"$d.status" || true
  echo "$name -> $(cat "$d.status")"
}
expect() { [ "$(cat "$EVD/$1.status")" = "$2" ] && echo "  PASS $1 status=$2" || { echo "  FAIL $1 status=$(cat "$EVD/$1.status") want=$2"; FAILS=$((FAILS+1)); }; }
expect_body() { grep -q -- "$2" "$EVD/$1.body" && echo "  PASS $1 body~/$2/" || { echo "  FAIL $1 body lacks /$2/"; FAILS=$((FAILS+1)); }; }

drive() {
  doctor >/dev/null
  local f="${1:?feature: health|models|management|chat-completions|cli}"; local B="http://127.0.0.1:$(st port)" K; K="$(st apikey)"; local M; M="$(st mgmtkey)"
  EVD="$(st evidence)/$f"; mkdir -p "$EVD"; FAILS=0
  case "$f" in
    health)
      req healthz "$B/healthz"; expect healthz 200
      req keepalive-noauth "$B/keep-alive"; ;;
    models)
      req models-noauth "$B/v1/models"; expect models-noauth 401
      req models-badkey -H "Authorization: Bearer wrong" "$B/v1/models"; expect models-badkey 401
      req models "$B/v1/models" -H "Authorization: Bearer $K"; expect models 200; expect_body models '"object"'
      req models-anthropic "$B/v1/models" -H "x-api-key: $K" -H "anthropic-version: 2023-06-01"; expect models-anthropic 200
      req gemini-models "$B/v1beta/models" -H "x-goog-api-key: $K"; expect gemini-models 200 ;;
    management)
      req mgmt-nokey "$B/v0/management/config"; expect mgmt-nokey 401
      req mgmt-config "$B/v0/management/config" -H "X-Management-Key: $M"; expect mgmt-config 200; expect_body mgmt-config '"api-keys"'
      req mgmt-debug-set -X PUT "$B/v0/management/debug" -H "Authorization: Bearer $M" -H 'Content-Type: application/json' -d '{"value":true}'; expect mgmt-debug-set 200
      req mgmt-debug-get "$B/v0/management/debug" -H "Authorization: Bearer $M"; expect mgmt-debug-get 200; expect_body mgmt-debug-get 'true'
      cp "$RUN/config.yaml" "$EVD/config-after.yaml"; 
      grep -q '^debug: true' "$RUN/config.yaml" && echo "  PASS config.yaml persisted debug: true" || { echo "  FAIL config.yaml not updated"; FAILS=$((FAILS+1)); } ;;
    chat-completions)
      req chat-noauth -X POST "$B/v1/chat/completions" -H 'Content-Type: application/json' -d '{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}'; expect chat-noauth 401
      req chat-nocreds -X POST "$B/v1/chat/completions" -H "Authorization: Bearer $K" -H 'Content-Type: application/json' -d '{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}'
      grep -q '"error"' "$EVD/chat-nocreds.body" && [ "$(cat "$EVD/chat-nocreds.status")" -ge 400 ] && echo "  PASS chat-nocreds returns structured error ($(cat "$EVD/chat-nocreds.status"))" || { echo "  FAIL chat-nocreds"; FAILS=$((FAILS+1)); } ;;
    cli)
      ( "$RUN/cli-proxy-api" -h ) >"$EVD/help.out" 2>&1; echo $? >"$EVD/help.exit"
      grep -q -- '-config' "$EVD/help.out" && echo "  PASS -h lists -config" || { echo "  FAIL -h"; FAILS=$((FAILS+1)); } ;;
    *) die "unknown feature $f" ;;
  esac
  tail -50 "$RUN/server.log" >"$EVD/server.log.tail"
  # Redact this run's ephemeral keys from every evidence file.
  find "$EVD" -type f -exec sed -i -e "s/$K/<api-key>/g" -e "s/$M/<mgmt-key>/g" {} +
  echo "evidence: $EVD  failures: $FAILS"; [ "$FAILS" = 0 ]
}

cleanup() {
  local pid; pid="$(st pid)"
  if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then kill "$pid"; for _ in $(seq 1 20); do kill -0 "$pid" 2>/dev/null || break; sleep 0.25; done; kill -9 "$pid" 2>/dev/null || true; fi
  local ev; ev="$(st evidence)"
  [ -n "$ev" ] && [ -f "$RUN/server.log" ] && sed -e "s/$(st apikey)/<api-key>/g" -e "s/$(st mgmtkey)/<mgmt-key>/g" "$RUN/server.log" >"$ev/server.log" || true
  rm -rf "$RUN"; echo "cleaned $RUN; evidence kept at ${ev:-<none>}"
}

case "${1:-}" in
  launch) launch ;; doctor) doctor ;; drive) shift; drive "$@" ;; cleanup) cleanup ;;
  all) shift; launch; doctor; rc=0; for f in "${@:-health models management chat-completions cli}"; do drive $f || rc=1; done; ev="$(st evidence)"; cleanup; ls "$ev" >/dev/null && exit $rc ;;
  *) echo "usage: $0 launch|doctor|drive <health|models|management|chat-completions|cli>|cleanup|all [features...]"; exit 2 ;;
esac
