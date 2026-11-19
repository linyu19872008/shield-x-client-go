#!/usr/bin/env bash
# 在 CI 中启动一个真实 shield-x，建站点/Token，然后跑 client-go 契约探针。
#
# 约定：
#   - 当前目录是 shield-x-client-go 仓库根
#   - 同级的 ./shield-x 是 shield-x 仓库（由 workflow checkout）
#   - MySQL 由 workflow service 提供
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SHIELDX="$ROOT/shield-x"
BIN="${RUNNER_TEMP:-/tmp}/shield-x-probe"
LOG="${RUNNER_TEMP:-/tmp}/shield-x-probe.log"
JAR="${RUNNER_TEMP:-/tmp}/shield-x-probe-cookies"
DB="${SHIELDX_PROBE_DB:-shield_x_probe}"

if [ ! -d "$SHIELDX" ]; then
  echo "缺少 $SHIELDX：workflow 需要先 checkout shield-x" >&2
  exit 1
fi

export SHIELDX_DB_DSN="root:11111111@tcp(127.0.0.1:3306)/${DB}?parseTime=true&loc=UTC"
export SHIELDX_SUPER_ADMIN_PASSWORD="Passw0rd!123"
export SHIELDX_LISSITE_AGENT="127.0.0.1:18080"
export SHIELDX_LISTEN_CONSOLE="127.0.0.1:18081"

cleanup() {
  if [ -n "${PID:-}" ]; then
    kill "$PID" 2>/dev/null || true
    wait "$PID" 2>/dev/null || true
  fi
  if [ -f "$SHIELDX/go.mod.ci-bak" ]; then
    mv "$SHIELDX/go.mod.ci-bak" "$SHIELDX/go.mod"
  fi
}
trap cleanup EXIT

echo "== build shield-x =="
# CI 已 checkout client-go 到同级目录：临时 replace，避免拉私有模块。
cp "$SHIELDX/go.mod" "$SHIELDX/go.mod.ci-bak"
printf '\nreplace github.com/linyu19872008/shield-x-client-go => ..\n' >> "$SHIELDX/go.mod"
(cd "$SHIELDX" && go build -o "$BIN" ./cmd/shield-x)

echo "== migrate =="
"$BIN" migrate >/tmp/shieldx-probe-migrate.log 2>&1 || {
  tail -30 /tmp/shieldx-probe-migrate.log
  exit 1
}

echo "== serve =="
"$BIN" serve >"$LOG" 2>&1 &
PID=$!

for _ in $(seq 1 60); do
  if curl -sf http://127.0.0.1:18080/shieldx/healthz >/dev/null 2>&1; then
    break
  fi
  sleep 0.5
done
curl -sf http://127.0.0.1:18080/shieldx/healthz >/dev/null || {
  echo "shield-x 未就绪" >&2
  tail -50 "$LOG" >&2
  exit 1
}

CONSOLE="http://127.0.0.1:18081/shieldx/console/api/v1"

echo "== login / change password =="
LOGIN="$(curl -sS -c "$JAR" -X POST "$CONSOLE/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"username":"super_admin","password":"Passw0rd!123"}')"
CSRF="$(printf '%s' "$LOGIN" | python3 -c 'import sys,json;print(json.load(sys.stdin)["csrf_token"])')"
curl -sS -b "$JAR" -X POST "$CONSOLE/auth/password" \
  -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
  -d '{"old_password":"Passw0rd!123","new_password":"Probe!Passw0rd123"}' >/dev/null
LOGIN="$(curl -sS -c "$JAR" -X POST "$CONSOLE/auth/login" \
  -H 'Content-Type: application/json' \
  -d '{"username":"super_admin","password":"Probe!Passw0rd123"}')"
CSRF="$(printf '%s' "$LOGIN" | python3 -c 'import sys,json;print(json.load(sys.stdin)["csrf_token"])')"

create_site() {
  local name="$1"
  curl -sS -b "$JAR" -X POST "$CONSOLE/sites" \
    -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
    -d "{\"name\":\"${name}\",\"generate_token\":true}"
}

echo "== create sites =="
SITE_A="$(create_site "ci-probe-a")"
SITE_B="$(create_site "ci-probe-b")"
TOK_A="$(printf '%s' "$SITE_A" | python3 -c 'import sys,json;print(json.load(sys.stdin)["token_plaintext"])')"
TOK_B="$(printf '%s' "$SITE_B" | python3 -c 'import sys,json;print(json.load(sys.stdin)["token_plaintext"])')"

echo "== run contract probe =="
cd "$ROOT"
SHIELDX_BASE_URL="http://127.0.0.1:18080/shieldx" \
SHIELDX_TOKEN="$TOK_A" \
SHIELDX_GATEWAY_ID="gw-ci-probe" \
SHIELDX_PROBE_EMPTY_TOKEN="$TOK_A" \
SHIELDX_PROBE_TOKEN_B="$TOK_B" \
go test -count=1 -tags=contract_probe -v ./probe
