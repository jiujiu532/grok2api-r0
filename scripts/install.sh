#!/usr/bin/env bash
# ============================================================
#  Grok2API 安装 / 管理脚本
#
#  适配本项目：config.yaml + ghcr.io/jiujiu532/grok2api-r0 +
#  WARP(socks5h) + FlareSolverr。
#
#  架构:
#    Client → Grok2API → socks5h://warp-N:1080 → WARP → Grok
#                     ↖ FlareSolverr 用同一代理刷 cf_clearance
#
#  一键安装（推荐，直接拉本仓库脚本；运行时只 pull 公开 GHCR 镜像）:
#    bash <(curl -fsSL https://raw.githubusercontent.com/jiujiu532/grok2api-r0/main/scripts/install.sh)
#
#  仓库内:
#    bash scripts/install.sh
#
#  说明:
#    - 安装脚本来自公开仓库 raw 地址，无需 Gist
#    - 更新应用只依赖镜像 ghcr.io/jiujiu532/grok2api-r0
# ============================================================

set -uo pipefail

# 安装脚本自身版本（与应用 VERSION 独立，仅表示脚本能力）
SCRIPT_VERSION="1.3.0"
INSTALL_DIR="${GROK2API_INSTALL_DIR:-/opt/grok2api}"
PROXY_DIR="${GROK2API_PROXY_DIR:-/opt/grok2api-proxy}"
# 仅 GHCR；可用 GROK2API_IMAGE 覆盖
GHCR_IMAGE_REPO="ghcr.io/jiujiu532/grok2api-r0"
IMAGE="${GROK2API_IMAGE:-}"
APP_NAME="grok2api"

resolve_default_image() {
  if [ -n "$IMAGE" ]; then
    return 0
  fi
  local ver=""
  # 仅当从仓库目录执行时才读本地 VERSION；远程一键安装走 latest。
  local script_dir
  script_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" 2>/dev/null && pwd || true)"
  if [ -n "$script_dir" ] && [ -f "$script_dir/image-tag.sh" ] && [ -f "$script_dir/../VERSION" ]; then
    ver=$(bash "$script_dir/image-tag.sh" < "$script_dir/../VERSION" 2>/dev/null || true)
  fi
  if [ -n "$ver" ] && [ "$ver" != "dev" ]; then
    IMAGE="${GHCR_IMAGE_REPO}:${ver}"
  else
    IMAGE="${GHCR_IMAGE_REPO}:latest"
  fi
}
APP_NETWORK="grok2api_net"
CONFIG_FILE="$INSTALL_DIR/config.yaml"
COMPOSE_FILE="$INSTALL_DIR/docker-compose.yml"
PROXY_COMPOSE_FILE="$PROXY_DIR/docker-compose.yml"
CREDENTIALS_FILE="$INSTALL_DIR/.bootstrap-credentials"
COMPOSE_CMD=""

# ============================================================
#  输出
# ============================================================
log()  { echo "$@"; }
ok()   { echo "  [OK]  $*"; }
info() { echo "  [-->] $*"; }
warn() { echo "  [!]   $*"; }
err()  { echo "  [ERR] $*" >&2; }

ask() {
  local prompt="$1" default="${2:-}" var
  if [ -n "$default" ]; then
    read -rp "  $prompt [默认 $default]: " var
    echo "${var:-$default}"
  else
    read -rp "  $prompt: " var
    echo "$var"
  fi
}

confirm() {
  local prompt="$1" default="${2:-Y}" hint var
  if [ "$default" = "Y" ]; then hint="Y/n"; else hint="y/N"; fi
  read -rp "  $prompt [$hint]: " var
  var="${var:-$default}"
  case "$var" in
    Y|y|YES|yes|Yes) return 0 ;;
    *) return 1 ;;
  esac
}

press_enter() {
  echo ""
  read -rp "  按 Enter 继续..." _
}

# ============================================================
#  依赖
# ============================================================
ensure_docker() {
  if ! command -v docker >/dev/null 2>&1; then
    err "未检测到 Docker。请先安装 Docker 后再运行本脚本。"
    err "文档: https://docs.docker.com/engine/install/"
    return 1
  fi
  if docker compose version >/dev/null 2>&1; then
    COMPOSE_CMD="docker compose"
    return 0
  fi
  if command -v docker-compose >/dev/null 2>&1; then
    COMPOSE_CMD="docker-compose"
    return 0
  fi
  err "未检测到 docker compose / docker-compose"
  return 1
}

require_openssl() {
  if command -v openssl >/dev/null 2>&1; then return 0; fi
  err "需要 openssl 生成密钥"
  return 1
}

compose_in() {
  local dir="$1"; shift
  (cd "$dir" && $COMPOSE_CMD "$@")
}

# ============================================================
#  状态
# ============================================================
is_installed() { [ -f "$COMPOSE_FILE" ] && [ -f "$CONFIG_FILE" ]; }

get_app_status() {
  if ! is_installed; then echo "未安装"; return; fi
  if docker ps --format '{{.Names}}' 2>/dev/null | grep -qE "^${APP_NAME}$"; then
    echo "运行中"
  else
    echo "已停止"
  fi
}

get_service_port() {
  [ -f "$COMPOSE_FILE" ] || { echo "8000"; return; }
  local p
  p=$(grep -oE '127\.0\.0\.1:[0-9]+:8000|"[0-9]+:8000"' "$COMPOSE_FILE" 2>/dev/null | head -1 | grep -oE '[0-9]+' | head -1 || true)
  echo "${p:-8000}"
}

count_warp() {
  docker ps --format '{{.Names}}' 2>/dev/null | grep -cE '^warp-[0-9]+$' || true
}

has_flaresolverr() {
  docker ps --format '{{.Names}}' 2>/dev/null | grep -qE '^flaresolverr$'
}

has_app_network() {
  docker network ls --format '{{.Name}}' 2>/dev/null | grep -qE "^${APP_NETWORK}$"
}

ensure_app_network() {
  if ! has_app_network; then
    docker network create "$APP_NETWORK" >/dev/null
    ok "已创建网络 $APP_NETWORK"
  fi
}

connect_to_app_network() {
  local name="$1"
  if docker ps --format '{{.Names}}' 2>/dev/null | grep -qE "^${name}$"; then
    if ! docker inspect "$name" --format '{{range $k,$v := .NetworkSettings.Networks}}{{$k}} {{end}}' 2>/dev/null | grep -q "$APP_NETWORK"; then
      docker network connect "$APP_NETWORK" "$name" 2>/dev/null || true
    fi
  fi
}

is_port_in_use() {
  local port="$1"
  if command -v ss >/dev/null 2>&1; then
    ss -ltn "( sport = :$port )" 2>/dev/null | grep -q LISTEN
  elif command -v netstat >/dev/null 2>&1; then
    netstat -ltn 2>/dev/null | grep -qE "[:.]$port[[:space:]]"
  else
    docker ps --format '{{.Ports}}' 2>/dev/null | grep -qE "[:.]$port->"
  fi
}

# ============================================================
#  代理套件: WARP + FlareSolverr
# ============================================================
generate_proxy_compose() {
  local out="$1" n="$2" i
  {
    echo "name: grok2api-proxy"
    echo "services:"
    for (( i=1; i<=n; i++ )); do
      cat <<EOF
  warp-$i:
    image: caomingjun/warp:latest
    container_name: warp-$i
    restart: unless-stopped
    environment:
      - WARP_SLEEP=2
    cap_add:
      - NET_ADMIN
    sysctls:
      - net.ipv6.conf.all.disable_ipv6=0
      - net.ipv4.conf.all.src_valid_mark=1
    networks:
      - app_net

EOF
    done
    cat <<EOF
  flaresolverr:
    image: ghcr.io/flaresolverr/flaresolverr:latest
    container_name: flaresolverr
    restart: unless-stopped
    ports:
      - "127.0.0.1:8191:8191"
    environment:
      TZ: Asia/Shanghai
      LOG_LEVEL: info
    networks:
      - app_net

networks:
  app_net:
    external: true
    name: $APP_NETWORK
EOF
  } > "$out"
}

generate_socks5_pool_text() {
  local n="$1" i
  for (( i=1; i<=n; i++ )); do
    echo "socks5h://warp-$i:1080"
  done
}

wait_warp_ready() {
  info "等待 WARP 初始化（最长 90s）..."
  local waited=0 probe
  while [ $waited -lt 90 ]; do
    probe=$(docker ps --format '{{.Names}}' 2>/dev/null | grep -E '^warp-[0-9]+$' | head -1 || true)
    if [ -n "$probe" ] && docker exec "$probe" curl -fsS --max-time 3 https://cloudflare.com/cdn-cgi/trace >/dev/null 2>&1; then
      ok "WARP 已就绪（${waited}s）"
      return 0
    fi
    sleep 3
    waited=$((waited + 3))
    echo -n "."
  done
  echo ""
  warn "WARP 未在 90s 内就绪，请检查: docker logs warp-1"
  return 1
}

setup_proxy_suite() {
  PROXY_POOL_TEXT=""
  FLARE_URL=""

  log ""
  log "[代理] WARP + FlareSolverr"
  log ""

  local existing
  existing=$(count_warp)
  if [ "${existing:-0}" -gt 0 ]; then
    ok "已有 $existing 个 WARP 实例"
    if has_flaresolverr; then
      ok "已有 FlareSolverr"
      FLARE_URL="http://flaresolverr:8191"
    fi
    PROXY_POOL_TEXT=$(generate_socks5_pool_text "$existing")
    ensure_app_network
    local i
    for (( i=1; i<=existing; i++ )); do connect_to_app_network "warp-$i"; done
    connect_to_app_network "flaresolverr"
    if confirm "是否复用现有代理套件" "Y"; then
      return 0
    fi
  fi

  if ! confirm "是否部署 WARP + FlareSolverr 代理套件（推荐，用于 IP/盾 403）" "Y"; then
    warn "已跳过代理套件。可稍后在菜单中单独部署。"
    return 0
  fi

  local warp_n
  while true; do
    warp_n=$(ask "WARP 实例数量 (1-6，每实例约 100MB)" "2")
    if [[ "$warp_n" =~ ^[1-6]$ ]]; then break; fi
    warn "请输入 1-6"
  done

  ensure_app_network
  mkdir -p "$PROXY_DIR"
  generate_proxy_compose "$PROXY_COMPOSE_FILE" "$warp_n"
  info "拉取代理镜像..."
  if ! compose_in "$PROXY_DIR" pull; then
    err "代理镜像拉取失败"
    return 1
  fi
  if ! compose_in "$PROXY_DIR" up -d; then
    err "代理套件启动失败"
    return 1
  fi
  ok "代理套件已启动"
  wait_warp_ready || true

  PROXY_POOL_TEXT=$(generate_socks5_pool_text "$warp_n")
  FLARE_URL="http://flaresolverr:8191"
  ok "SOCKS5 出口: $warp_n 个 (socks5h://warp-N:1080)"
  ok "FlareSolverr: $FLARE_URL"
  return 0
}

# ============================================================
#  应用 compose + config.yaml
# ============================================================
generate_app_compose() {
  local port="$1"
  cat > "$COMPOSE_FILE" <<EOF
name: grok2api

services:
  grok2api:
    container_name: $APP_NAME
    image: $IMAGE
    ports:
      - "127.0.0.1:${port}:8000"
    environment:
      TZ: Asia/Shanghai
    volumes:
      - ./config.yaml:/run/grok2api/config.yaml:ro
      - grok2api-data:/app/data
    restart: unless-stopped
    init: true
    stop_grace_period: 30s
    security_opt:
      - no-new-privileges:true
    networks:
      - app_net

networks:
  app_net:
    external: true
    name: $APP_NETWORK

volumes:
  grok2api-data:
EOF
}

generate_config_yaml() {
  local jwt_secret="$1" enc_key="$2" admin_user="$3" admin_pass="$4"
  cat > "$CONFIG_FILE" <<EOF
# 由 scripts/install.sh 生成 — 请妥善备份，尤其是 credentialEncryptionKey
server:
  listen: "0.0.0.0:8000"
  maxBodyBytes: 33554432
  readTimeout: 15m
  requestTimeout: 2h
  swaggerEnabled: false

auth:
  accessTokenTTL: 15m
  refreshTokenTTL: 720h
  secureCookies: ${SECURE_COOKIES}

secrets:
  jwtSecret: "${jwt_secret}"
  credentialEncryptionKey: "${enc_key}"

bootstrapAdmin:
  username: "${admin_user}"
  password: "${admin_pass}"

frontend:
  staticPath: "./frontend/dist"

database:
  driver: sqlite
  sqlite:
    path: "./data/backend.db"
  postgres:
    dsn: "postgres://user:password@127.0.0.1:5432/grok2api?sslmode=disable"
    maxOpenConns: 50
    maxIdleConns: 10

runtimeStore:
  driver: memory
  redis:
    address: "127.0.0.1:6379"
    username: ""
    password: ""
    database: 0
    keyPrefix: "grok2api:"
    tls: false

media:
  driver: local
  local:
    path: "./data/media"
EOF
  if ! chmod 600 "$CONFIG_FILE"; then
    err "无法限制 config.yaml 权限"
    return 1
  fi
}

write_post_install_guide() {
  local port="$1"
  local auto_cfg="${2:-0}"
  local guide="$INSTALL_DIR/POST_INSTALL.txt"
  {
    echo "Grok2API 安装完成"
    echo "========================================"
    echo ""
    echo "1) 打开管理端: http://127.0.0.1:${port}"
    echo "2) 使用 bootstrap 管理员登录（见 $CREDENTIALS_FILE）"
    if [ "$auto_cfg" = "1" ]; then
      echo "3) 出口代理 + Clearance 已由安装脚本自动写入（WARP + FlareSolverr）"
      echo "   可在「设置 → 出口代理 / 运行策略」核对"
    else
      echo "3) 设置 → 出口代理：为 Grok Web / Grok Console 添加节点"
      if [ -n "${PROXY_POOL_TEXT:-}" ]; then
        echo "   推荐代理地址（容器内网络）:"
        echo "$PROXY_POOL_TEXT" | while read -r line; do
          [ -n "$line" ] && echo "     - $line"
        done
      fi
      echo "4) 设置 → 运行策略 → 防封/Clearance:"
      if [ -n "${FLARE_URL:-}" ]; then
        echo "     - 模式: FlareSolverr"
        echo "     - 地址: $FLARE_URL"
        echo "     - 点击「立即刷新」"
      fi
    fi
    echo "5) 上游账号中导入 Grok Web/Build/Console 账号"
    echo "6) 客户端密钥中创建 g2a_ API Key，调用 /v1/*"
    echo ""
    echo "说明: 出口节点与 Clearance 存在数据库（管理端热加载），不在 config.yaml。"
    echo "      首次创建管理员后，建议修改密码并从 config.yaml 删除 bootstrapAdmin。"
  } > "$guide"
}

# 安装后通过管理 API 自动写入出口节点 + Clearance（需服务已健康）
# 依赖: curl + python3
bootstrap_runtime_proxy_config() {
  local port="$1" user="$2" pass="$3"
  local base="http://127.0.0.1:${port}"
  local token settings_json revised body

  if ! command -v curl >/dev/null 2>&1; then
    warn "缺少 curl，跳过自动写入出口/Clearance"
    return 1
  fi
  if ! command -v python3 >/dev/null 2>&1; then
    warn "缺少 python3，跳过自动写入出口/Clearance"
    return 1
  fi

  info "通过管理 API 自动配置出口代理与 Clearance..."

  # 登录
  token=$(
    GROK2API_BOOTSTRAP_USER="$user" GROK2API_BOOTSTRAP_PASSWORD="$pass" \
    python3 - "$base" <<'PY'
import json, os, sys, urllib.request, urllib.error
base = sys.argv[1]
user = os.environ["GROK2API_BOOTSTRAP_USER"]
password = os.environ["GROK2API_BOOTSTRAP_PASSWORD"]
req = urllib.request.Request(
    base.rstrip("/") + "/api/admin/v1/auth/login",
    data=json.dumps({"username": user, "password": password}).encode(),
    headers={"Content-Type": "application/json"},
    method="POST",
)
try:
    with urllib.request.urlopen(req, timeout=20) as resp:
        data = json.load(resp)
    print(data["data"]["tokens"]["accessToken"])
except Exception as exc:
    sys.stderr.write(f"login failed: {exc}\n")
    sys.exit(1)
PY
  ) || {
    warn "管理员登录失败，请稍后手动在管理端配置出口/Clearance"
    return 1
  }

  # 写 Clearance + 创建缺失的出口节点 + 触发刷新
  PROXY_POOL_TEXT="${PROXY_POOL_TEXT:-}" FLARE_URL="${FLARE_URL:-}" \
  python3 - "$base" "$token" <<'PY'
import json, os, sys, urllib.request, urllib.error

base = sys.argv[1].rstrip("/")
token = sys.argv[2]
pool = [line.strip() for line in os.environ.get("PROXY_POOL_TEXT", "").splitlines() if line.strip()]
flare = os.environ.get("FLARE_URL", "").strip() or "http://flaresolverr:8191"

def api(method, path, body=None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(
        base + path,
        data=data,
        method=method,
        headers={
            "Authorization": f"Bearer {token}",
            "Content-Type": "application/json",
            "Accept": "application/json",
        },
    )
    with urllib.request.urlopen(req, timeout=60) as resp:
        raw = resp.read()
        if not raw:
            return None
        return json.loads(raw)

# 1) 更新 Clearance
snap = api("GET", "/api/admin/v1/settings")["data"]
cfg = snap["config"]
rev = snap["revision"]
clearance = cfg.get("clearance") or {}
clearance.update({
    "mode": "flaresolverr",
    "flareSolverrURL": flare,
    "userAgent": clearance.get("userAgent") or (
        "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) "
        "AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
    ),
    "timeout": clearance.get("timeout") or "60s",
    "refreshInterval": clearance.get("refreshInterval") or "1h",
    "clientHintsEnabled": True if clearance.get("clientHintsEnabled") is None else clearance.get("clientHintsEnabled"),
    "antiBotCooldown": clearance.get("antiBotCooldown") or "45s",
    "cfCookiesConfigured": bool(clearance.get("cfCookiesConfigured")),
})
# 保留已配置 CF cookie：不传 cfCookies 字段即可由后端按 configured 保留
if "cfCookies" in clearance:
    clearance.pop("cfCookies", None)
cfg["clearance"] = clearance
api("PUT", "/api/admin/v1/settings", {"revision": rev, "config": cfg})
print("clearance=ok", flush=True)

# 2) 创建出口节点（Web / Console / WebAsset）
existing = api("GET", "/api/admin/v1/egress-nodes")["data"]["items"]
existing_keys = {(n.get("name"), n.get("scope")) for n in existing}
created = 0
scopes = [
    ("grok_web", "Web"),
    ("grok_console", "Console"),
    ("grok_web_asset", "WebAsset"),
]
for idx, proxy in enumerate(pool, start=1):
    short = proxy.split("://")[-1].split(":")[0]  # warp-1
    for scope, label in scopes:
        name = f"{short}-{label}"
        if (name, scope) in existing_keys:
            continue
        api("POST", "/api/admin/v1/egress-nodes", {
            "name": name,
            "scope": scope,
            "enabled": True,
            "proxyURL": proxy,
            "userAgent": "",
        })
        created += 1
print(f"egress_created={created}", flush=True)

# 3) 触发 FlareSolverr 刷新（失败不致命）
try:
    result = api("POST", "/api/admin/v1/settings/clearance/refresh", {})
    data = (result or {}).get("data") or {}
    print(f"clearance_refresh=ok updated={data.get('updated', 0)} failed={data.get('failed', 0)}", flush=True)
except Exception as exc:
    print(f"clearance_refresh=skip {exc}", flush=True)
PY
  local rc=$?
  if [ $rc -eq 0 ]; then
    ok "已自动写入 Clearance(FlareSolverr) 与出口节点"
    return 0
  fi
  warn "自动写入运行配置失败，请按 POST_INSTALL.txt 手动配置"
  return 1
}

# ============================================================
#  命令
# ============================================================
cmd_install() {
  log ""
  log "========================================="
  log "  安装 Grok2API  v$SCRIPT_VERSION"
  log "========================================="

  if is_installed; then
    warn "检测到已安装: $INSTALL_DIR；不会覆盖 config.yaml 或加密密钥"
    info "请使用 update 拉取并重启现有安装"
    return 0
  fi

  log ""
  log "[1/5] 前置检查..."
  ensure_docker || return 1
  require_openssl || return 1
  ok "Docker / Compose 可用 ($COMPOSE_CMD)"

  setup_proxy_suite || return 1

  log ""
  log "[2/5] 应用参数..."
  local admin_user admin_pass service_port jwt_secret enc_key
  admin_user=$(ask "管理员用户名" "admin")
  while true; do
    admin_pass=$(ask "管理员密码（至少 8 位）" "")
    if [ ${#admin_pass} -lt 8 ]; then
      warn "密码过短"
      continue
    fi
    break
  done

  if confirm "是否通过 HTTPS 反向代理提供管理端" "Y"; then
    SECURE_COOKIES=true
  else
    SECURE_COOKIES=false
    warn "本地 HTTP 登录将使用非安全 Cookie；HTTPS 反代时请设为 true"
  fi

  while true; do
    service_port=$(ask "宿主机端口" "8000")
    if [[ ! "$service_port" =~ ^[0-9]+$ ]] || [ "$service_port" -lt 1 ] || [ "$service_port" -gt 65535 ]; then
      warn "端口无效"
      continue
    fi
    if is_port_in_use "$service_port"; then
      warn "端口 $service_port 已被占用"
      if ! confirm "仍要使用" "N"; then continue; fi
    fi
    break
  done

  jwt_secret=$(openssl rand -hex 32)
  enc_key=$(openssl rand -base64 32)

  log ""
  log "[3/5] 写入配置..."
  umask 077
  mkdir -p "$INSTALL_DIR"
  if ! chmod 700 "$INSTALL_DIR"; then
    err "无法限制安装目录权限"
    return 1
  fi
  ensure_app_network
  generate_config_yaml "$jwt_secret" "$enc_key" "$admin_user" "$admin_pass" || return 1
  generate_app_compose "$service_port"
  cat > "$CREDENTIALS_FILE" <<EOF
admin_username=$admin_user
admin_password=$admin_pass
port=$service_port
jwt_secret=$jwt_secret
credential_encryption_key=$enc_key
installed_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)
script_version=$SCRIPT_VERSION
EOF
  if ! chmod 600 "$CREDENTIALS_FILE"; then
    err "无法限制凭据文件权限"
    return 1
  fi
  echo "$SCRIPT_VERSION" > "$INSTALL_DIR/.install-version"
  ok "config.yaml / docker-compose.yml 已生成"

  log ""
  log "[4/5] 拉取镜像 $IMAGE ..."
  if ! docker pull "$IMAGE"; then
    err "镜像拉取失败"
    return 1
  fi

  log ""
  log "[5/5] 启动服务..."
  if ! compose_in "$INSTALL_DIR" up -d; then
    err "启动失败，请检查: docker logs $APP_NAME"
    return 1
  fi
  connect_to_app_network "$APP_NAME"

  info "等待健康检查 /healthz ..."
  local wait_secs=0 healthy=0
  while [ $wait_secs -lt 45 ]; do
    if curl -fsS --max-time 2 "http://127.0.0.1:${service_port}/healthz" >/dev/null 2>&1; then
      healthy=1
      break
    fi
    sleep 2
    wait_secs=$((wait_secs + 2))
    echo -n "."
  done
  echo ""

  local auto_cfg=0
  if [ $healthy -eq 1 ] && [ -n "${PROXY_POOL_TEXT:-}" ]; then
    # 额外等 ready，确保管理员与 DB 就绪
    local r=0
    while [ $r -lt 30 ]; do
      if curl -fsS --max-time 2 "http://127.0.0.1:${service_port}/readyz" >/dev/null 2>&1; then
        break
      fi
      sleep 2
      r=$((r + 2))
    done
    if bootstrap_runtime_proxy_config "$service_port" "$admin_user" "$admin_pass"; then
      auto_cfg=1
    fi
  fi
  write_post_install_guide "$service_port" "$auto_cfg"

  log ""
  log "========================================="
  if [ $healthy -eq 1 ]; then
    ok "安装完成"
  else
    warn "健康检查超时，请: docker logs $APP_NAME"
  fi
  log "========================================="
  log ""
  log "  管理端:     http://127.0.0.1:${service_port}"
  log "  管理员账号与密码已写入受限凭据文件"
  log "  安装目录:   $INSTALL_DIR"
  log "  凭据备份:   $CREDENTIALS_FILE"
  log "  后续步骤:   $INSTALL_DIR/POST_INSTALL.txt"
  if [ -n "${PROXY_POOL_TEXT:-}" ]; then
    log "  代理池:"
    echo "$PROXY_POOL_TEXT" | sed 's/^/    /'
  fi
  [ -n "${FLARE_URL:-}" ] && log "  FlareSolverr: $FLARE_URL"
  if [ "$auto_cfg" = "1" ]; then
    log "  运行配置:   已自动写入出口节点 + Clearance(FlareSolverr)"
  elif [ -n "${PROXY_POOL_TEXT:-}" ]; then
    log "  运行配置:   请按 POST_INSTALL.txt 手动配置出口/Clearance"
  fi
  log ""
  log "  外网访问请用 Nginx/Caddy 反代到 127.0.0.1:${service_port}"
  log "  下一步: 登录管理端导入上游账号，并创建 g2a_ API Key"
  log ""
}

cmd_status() {
  log ""
  log "========================================="
  log "  Grok2API 状态"
  log "========================================="
  log ""
  log "  安装:       $(is_installed && echo "是 ($INSTALL_DIR)" || echo "否")"
  log "  应用:       $(get_app_status)"
  log "  端口:       $(get_service_port)"
  log "  镜像:       $IMAGE"
  log "  WARP 实例:  $(count_warp)"
  if has_flaresolverr; then
    log "  FlareSolverr: 运行中 (http://127.0.0.1:8191)"
  else
    log "  FlareSolverr: 未运行"
  fi
  log "  网络:       $APP_NETWORK $(has_app_network && echo "存在" || echo "不存在")"
  if is_installed; then
    log ""
    docker ps --filter "name=${APP_NAME}" --filter "name=warp-" --filter "name=flaresolverr" \
      --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}" 2>/dev/null || true
  fi
  log ""
}

cmd_update() {
  if ! is_installed; then warn "未安装"; return 0; fi
  ensure_docker || return 1
  log ""
  log "  当前镜像: $IMAGE"
  if ! confirm "拉取最新镜像并滚动更新" "Y"; then info "已取消"; return 0; fi
  cp "$COMPOSE_FILE" "${COMPOSE_FILE}.bak" 2>/dev/null || true
  docker pull "$IMAGE" || return 1
  compose_in "$INSTALL_DIR" up -d || return 1
  connect_to_app_network "$APP_NAME"
  ok "更新完成"
}

cmd_restart() {
  if ! is_installed; then warn "未安装"; return 0; fi
  ensure_docker || return 1
  log ""
  log "  将重启应用与代理（WARP 重启会换 IP）"
  if ! confirm "确认" "Y"; then info "已取消"; return 0; fi
  if [ -f "$PROXY_COMPOSE_FILE" ]; then
    info "重启代理套件..."
    compose_in "$PROXY_DIR" restart 2>/dev/null || compose_in "$PROXY_DIR" up -d || true
    sleep 8
  fi
  info "重启 Grok2API..."
  compose_in "$INSTALL_DIR" restart || compose_in "$INSTALL_DIR" up -d || return 1
  ok "已重启"
}

cmd_logs() {
  ensure_docker || return 1
  docker logs -f --tail 200 "$APP_NAME"
}

cmd_proxy_only() {
  ensure_docker || return 1
  setup_proxy_suite
  log ""
  log "  部署完成后请到管理端:"
  log "    1. 出口代理添加 socks5h://warp-N:1080"
  log "    2. Clearance 模式选 FlareSolverr，地址 http://flaresolverr:8191"
  log "       （应用与代理必须在同一 Docker 网络 $APP_NETWORK）"
  if is_installed; then
    connect_to_app_network "$APP_NAME"
    ok "已确保 $APP_NAME 加入 $APP_NETWORK"
  fi
}

cmd_uninstall() {
  if ! is_installed && [ ! -f "$PROXY_COMPOSE_FILE" ]; then
    warn "未检测到安装"
    return 0
  fi
  ensure_docker || return 1
  log ""
  warn "卸载不会默认删除 Docker 数据卷（账号/媒体）"
  if ! confirm "停止并删除应用容器" "N"; then info "已取消"; return 0; fi
  if [ -f "$COMPOSE_FILE" ]; then
    compose_in "$INSTALL_DIR" down || true
  fi
  if [ -f "$PROXY_COMPOSE_FILE" ] && confirm "同时卸载 WARP + FlareSolverr" "N"; then
    compose_in "$PROXY_DIR" down || true
  fi
  if confirm "删除安装目录 $INSTALL_DIR（含 config.yaml）" "N"; then
    rm -rf "$INSTALL_DIR"
  fi
  if confirm "删除 Docker 数据卷 grok2api_grok2api-data（不可恢复）" "N"; then
    docker volume rm grok2api_grok2api-data 2>/dev/null || true
  fi
  ok "卸载流程结束"
}

# ============================================================
#  菜单
# ============================================================
show_menu() {
  clear 2>/dev/null || true
  log "========================================="
  log "  Grok2API 管理工具  v$SCRIPT_VERSION"
  log "========================================="
  log "  应用状态: $(get_app_status)"
  log "  WARP:     $(count_warp)  个"
  log "  目录:     $INSTALL_DIR"
  log "========================================="
  log ""
  if ! is_installed; then
    log "  1) 安装 Grok2API（可选 WARP + FlareSolverr）"
    log "  2) 仅部署代理套件"
    log "  0) 退出"
  else
    log "  1) 状态"
    log "  2) 更新镜像"
    log "  3) 重启并换 IP"
    log "  4) 查看日志"
    log "  5) 部署/重建代理套件"
    log "  6) 卸载"
    log "  0) 退出"
  fi
  log ""
}

main() {
  resolve_default_image
  # 非交互参数
  case "${1:-}" in
    install) ensure_docker && cmd_install; return $? ;;
    status)  ensure_docker && cmd_status; return $? ;;
    update)  ensure_docker && cmd_update; return $? ;;
    restart) ensure_docker && cmd_restart; return $? ;;
    logs)    ensure_docker && cmd_logs; return $? ;;
    proxy)   ensure_docker && cmd_proxy_only; return $? ;;
    uninstall) ensure_docker && cmd_uninstall; return $? ;;
  esac

  while true; do
    show_menu
    local choice
    read -rp "  请选择: " choice
    case "$choice" in
      1)
        if ! is_installed; then ensure_docker && cmd_install; else ensure_docker && cmd_status; fi
        press_enter
        ;;
      2)
        if ! is_installed; then ensure_docker && cmd_proxy_only; else ensure_docker && cmd_update; fi
        press_enter
        ;;
      3)
        if is_installed; then ensure_docker && cmd_restart; press_enter; fi
        ;;
      4)
        if is_installed; then ensure_docker && cmd_logs; fi
        ;;
      5)
        if is_installed; then ensure_docker && cmd_proxy_only; press_enter; fi
        ;;
      6)
        if is_installed; then ensure_docker && cmd_uninstall; press_enter; fi
        ;;
      0|q|Q)
        exit 0
        ;;
      *)
        warn "无效选项"
        sleep 1
        ;;
    esac
  done
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
