#!/usr/bin/env bash
# AI Gateway Poller —— 一键安装脚本
#
# 交互式用法：
#   bash install.sh
#
# 非交互式（CI / 自动化）：
#   ADMIN_USER=admin ADMIN_PASSWORD='12位以上密码' bash install.sh
#
# 脚本做的事情：
#   1. 检查 docker / docker compose / openssl / curl 是否可用
#   2. 交互读取管理员用户名和密码（或从环境变量取）
#   3. 随机生成 PROXY_TOKEN，把三项一起写到 .env（chmod 600）
#   4. 额外写一份明文备份到 credentials.txt（chmod 600）
#   5. docker compose build && up -d
#   6. 轮询 /healthz 直到就绪或超时
#   7. 打印访问 URL 和后续步骤

set -euo pipefail

# ── 颜色与日志助手 ───────────────────────────────────────────
if [[ -t 1 ]]; then
  RED=$'\033[31m'; GREEN=$'\033[32m'; YEL=$'\033[33m'; BLUE=$'\033[34m'; BOLD=$'\033[1m'; NC=$'\033[0m'
else
  RED=''; GREEN=''; YEL=''; BLUE=''; BOLD=''; NC=''
fi
info()  { printf '%s==>%s %s\n' "$GREEN" "$NC" "$*"; }
warn()  { printf '%swarn:%s %s\n' "$YEL" "$NC" "$*" >&2; }
fatal() { printf '%serror:%s %s\n' "$RED" "$NC" "$*" >&2; exit 1; }

# ── 1. 前提检查 ──────────────────────────────────────────────
info "检查依赖"
command -v docker  >/dev/null 2>&1 || fatal "缺少 docker。安装说明：https://docs.docker.com/engine/install/"
docker compose version >/dev/null 2>&1 || fatal "缺少 docker compose v2 插件（老版 docker-compose 不支持，要装新的 plugin）"
command -v openssl >/dev/null 2>&1 || fatal "缺少 openssl"
command -v curl    >/dev/null 2>&1 || fatal "缺少 curl"

# 必须在仓库根目录跑
[[ -f docker-compose.yml && -f Dockerfile ]] || fatal "请在仓库根目录运行本脚本（当前目录：$(pwd)）"

# ── 2. 幂等性保护 ────────────────────────────────────────────
if [[ -f .env ]]; then
  warn ".env 已存在 —— 如果覆盖，旧的管理员密码将作废。"
  read -rp "覆盖 .env？[y/N] " ans
  [[ "$ans" =~ ^[Yy]$ ]] || { info "已中止，未做任何修改"; exit 0; }
fi

# ── 3. 读取凭证 ──────────────────────────────────────────────
ADMIN_USER="${ADMIN_USER:-}"
if [[ -z "$ADMIN_USER" ]]; then
  read -rp "管理员用户名 [admin]: " ADMIN_USER
  ADMIN_USER="${ADMIN_USER:-admin}"
fi

ADMIN_PASSWORD="${ADMIN_PASSWORD:-}"
if [[ -n "$ADMIN_PASSWORD" ]]; then
  # 环境变量方式
  if (( ${#ADMIN_PASSWORD} < 12 )); then
    fatal "ADMIN_PASSWORD 至少 12 位（当前 ${#ADMIN_PASSWORD} 位）"
  fi
else
  # 交互方式
  while :; do
    read -rsp "管理员密码（至少 12 位，输入时不回显）: " ADMIN_PASSWORD; echo
    if (( ${#ADMIN_PASSWORD} < 12 )); then
      warn "太短，至少 12 位，请重新输入"
      continue
    fi
    read -rsp "再输一遍确认: " confirm; echo
    if [[ "$ADMIN_PASSWORD" != "$confirm" ]]; then
      warn "两次不一致，请重新输入"
      continue
    fi
    break
  done
fi

info "生成随机 PROXY_TOKEN（64 位十六进制）"
PROXY_TOKEN=$(openssl rand -hex 32)

# ── 4. 写入配置 ──────────────────────────────────────────────
info "写入 .env"
# 用 printf 而不是 heredoc，避免密码里含 $ / 反引号 / \ 被二次展开
printf 'ADMIN_USER=%s\nADMIN_PASSWORD=%s\nPROXY_TOKEN=%s\n' \
  "$ADMIN_USER" "$ADMIN_PASSWORD" "$PROXY_TOKEN" > .env
chmod 600 .env

CRED_FILE="$(pwd)/credentials.txt"
info "写入凭证备份 $CRED_FILE"
cat > "$CRED_FILE" <<EOF
AI Gateway Poller —— 凭证备份
生成时间 : $(date -Is)

管理后台 URL : http://<本机 IP 或域名>:9090/
管理员用户名 : $ADMIN_USER
管理员密码   : $ADMIN_PASSWORD

SDK base_url : http://<本机 IP 或域名>:9090/v1
SDK api_key  : $PROXY_TOKEN

⚠  这个文件包含明文凭证：
   - 已设为 mode 600，只有当前用户可读
   - 已被仓库 .gitignore 排除，不会被误 git commit
   - 建议把内容存进密码管理器后删掉本文件：
       shred -u $CRED_FILE
EOF
chmod 600 "$CRED_FILE"

# ── 5. 启动容器 ──────────────────────────────────────────────
info "构建镜像（第一次可能要 1~2 分钟下载依赖）"
docker compose build

info "启动服务"
docker compose up -d

# ── 6. 等待 healthz ──────────────────────────────────────────
info "等待 /healthz 就绪"
HEALTHZ_URL="http://127.0.0.1:9090/healthz"
for i in {1..30}; do
  if curl -fsS "$HEALTHZ_URL" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

if ! curl -fsS "$HEALTHZ_URL" >/dev/null 2>&1; then
  warn "服务 30 秒内没响应。排查命令："
  printf '    docker compose ps\n    docker compose logs app\n' >&2
  exit 1
fi

# ── 7. 总结 ──────────────────────────────────────────────────
HOST_IP=$(hostname -I 2>/dev/null | awk '{print $1}' || true)
HOST_IP=${HOST_IP:-127.0.0.1}
echo
printf '%s%s==================================================%s\n' "$BOLD" "$GREEN" "$NC"
printf '%s  部署完成 ✓%s\n' "$BOLD" "$NC"
printf '%s%s==================================================%s\n' "$BOLD" "$GREEN" "$NC"
echo
echo "  管理后台   : http://$HOST_IP:9090/"
echo "  用户名     : $ADMIN_USER"
echo "  密码       : 见 $CRED_FILE"
echo
echo "  SDK base_url : http://$HOST_IP:9090/v1"
echo "  SDK api_key  : 见 $CRED_FILE 里的 PROXY_TOKEN"
echo
printf '%s下一步：%s\n' "$BLUE" "$NC"
echo "  1) 浏览器打开上面的管理后台 URL，添加你的 Vercel AI Gateway Key"
echo "  2) 把凭证存到密码管理器，然后删掉 credentials.txt"
echo "  3) 想加 HTTPS 看 README 的「加 HTTPS」章节"
echo
printf '%s%s==================================================%s\n' "$BOLD" "$GREEN" "$NC"
