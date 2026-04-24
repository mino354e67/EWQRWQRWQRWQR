# AI Gateway 密钥轮询器

自部署的 [Vercel AI Gateway](https://vercel.com/ai-gateway) 密钥池代理：
管理一组 Gateway API Key，跟踪每把 Key 的月度消耗，到达阈值后自动暂停，
并对外暴露一个 OpenAI 兼容的 `/v1` 端点 —— 每次请求挑当前花费最少且未
暂停的那把 Key，透明转发到上游。

---

## 快速开始

前提：一台 Linux 机器（Debian/Ubuntu/RHEL 系都行），装好了 **Docker** 和
**docker compose v2**。还没装？见下面的「前置依赖」。

```bash
git clone https://github.com/mino354e67/ewqrwqrwqrwqr.git ai-gateway
cd ai-gateway
bash install.sh
```

`install.sh` 会交互式问你管理员用户名密码、生成随机 `PROXY_TOKEN`、写 `.env`
和 `credentials.txt`（都 `chmod 600`）、起容器、等健康检查通过，最后打印后续
步骤。

**注意**：容器默认**只绑在 `127.0.0.1:9090`**，从外部访问必须配反向代理。
这是故意的 —— 直接把 9090 暴露到公网上、没 HTTPS 没速率限制前的 Nginx/Caddy
保护，是在拿你的 Gateway 余额赌。所以完整流程是：

1. 跑完 `install.sh`，在 VPS 上自己 `curl http://127.0.0.1:9090/healthz` 验证
2. 配反向代理（下节三选一）
3. 在 SDK 里用 `https://<你的域名>/v1`

> **非交互式**（脚本 / CI）：
> `ADMIN_USER=admin ADMIN_PASSWORD='your-12+chars' bash install.sh`

---

## 加 HTTPS（可选）

容器默认只监听 `127.0.0.1:9090`，不对公网暴露。要让外面访问，挑一种反向代理。

### 方案 A：Caddy（最省事，证书自动）

```bash
# Debian / Ubuntu：安装 Caddy 官方源
sudo apt install -y debian-keyring debian-archive-keyring apt-transport-https curl
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
  | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
  | sudo tee /etc/apt/sources.list.d/caddy-stable.list >/dev/null
sudo apt update && sudo apt install -y caddy

# 写配置（记得把 gateway.example.com 换成你的域名）
sudo tee /etc/caddy/Caddyfile >/dev/null <<'EOF'
gateway.example.com {
    reverse_proxy 127.0.0.1:9090
}
EOF
sudo systemctl reload caddy
```

搞定后访问 `https://gateway.example.com`，证书 Caddy 会自动签。

### 方案 B：Cloudflare Tunnel（不用开 80/443 端口）

适合 VPS 被封 80 端口、或者不想暴露公网 IP 的场景。在 Cloudflare
Zero Trust 控制台建一条 Tunnel，指向 `http://127.0.0.1:9090` 即可。

### 方案 C：现有 Nginx

```nginx
server {
    listen 443 ssl http2;
    server_name gateway.example.com;

    # ssl_certificate 等配置略 —— 自己用 certbot 签
    ssl_certificate     /etc/letsencrypt/live/gateway.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/gateway.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:9090;
        proxy_set_header Host              $host;
        proxy_set_header X-Forwarded-For   $remote_addr;  # 限流器需要
        proxy_set_header X-Real-IP         $remote_addr;

        # SSE 流式转发
        proxy_buffering off;
        proxy_read_timeout 600s;
    }
}
```

---

## 在 SDK 中使用

拿到 `credentials.txt` 里的 `PROXY_TOKEN`，填到任何 OpenAI 兼容的客户端里就行：

```python
from openai import OpenAI

client = OpenAI(
    base_url="https://gateway.example.com/v1",   # 你的域名
    api_key="<credentials.txt 里的 PROXY_TOKEN>",
)
resp = client.chat.completions.create(
    model="anthropic/claude-sonnet-4.5",
    messages=[{"role": "user", "content": "你好"}],
)
print(resp.choices[0].message.content)
```

Cursor / Cline / LobeChat / OpenWebUI / Continue 等任何能填
`Base URL` + `API Key` 的客户端，都按这个格式填。代理会剥掉你发来的
`PROXY_TOKEN`，换成池子里的某一把真 Gateway Key 再转发上游 ——
真正的 Gateway Key 永远不离开服务器。

---

## 运维

所有命令都在仓库目录下执行。

```bash
# 查看日志（含 auth_fail 失败认证记录）
docker compose logs -f app

# 重启
docker compose restart app

# 拉最新代码并重建
git pull && docker compose up -d --build

# 轮换 PROXY_TOKEN（记得同步所有 SDK 客户端的 api_key）
bash install.sh           # 会问是否覆盖 .env，选 y 重新生成

# 停止（保留数据卷，Key 不会丢）
docker compose down

# 彻底清空（⚠️ 会丢失所有保存的 Gateway Key）
docker compose down -v
```

---

## 前置依赖

### 安装 Docker + Compose（Debian / Ubuntu）

如果机器上还没有：

```bash
curl -fsSL https://get.docker.com | sudo sh
sudo usermod -aG docker "$USER"       # 让当前用户不用 sudo 也能 docker
newgrp docker                         # 立即生效（或退出再登录）
```

装完验证：

```bash
docker compose version                # 应打印 Docker Compose version v2.x
```

---

## 配置

### 环境变量一览

`install.sh` 帮你写好了前三项，其他可选项如果需要，直接编辑 `.env` 后
`docker compose up -d` 即可生效。

| 变量                   | 必填 | 默认                                         | 说明                                       |
| ---------------------- | ---- | -------------------------------------------- | ------------------------------------------ |
| `ADMIN_USER`           | 是   | —                                            | 管理后台用户名                             |
| `ADMIN_PASSWORD`       | 是   | —（至少 12 位）                              | 管理后台密码                               |
| `PROXY_TOKEN`          | 是   | —（至少 24 位，建议 `openssl rand -hex 32`） | `/v1` 代理的 Bearer Token                  |
| `GATEWAY_BASE_URL`     | 否   | `https://ai-gateway.vercel.sh/v1`            | 上游 Gateway 地址                          |
| `MONTHLY_COOLDOWN_USD` | 否   | `5`                                          | 单把 Key 月消耗达到此值自动暂停            |
| `AUTH_FAIL_LIMIT`      | 否   | `10`                                         | 60 秒内失败认证次数阈值，`0` 关闭限流      |
| `AUTH_BLOCK_MINUTES`   | 否   | `15`                                         | 触发限流后的封禁时长                       |
| `LISTEN_ADDR`          | 否   | `:9090`                                      | 绑定地址                                   |

必填项缺失或长度不够时，容器启动会立即退出并打印错误 —— 这是故意的，防止
在公网部署时裸奔。

### 端点一览

| 路径                              | 认证                          | 用途                                |
| --------------------------------- | ----------------------------- | ----------------------------------- |
| `/`                               | HTTP Basic                    | 管理后台 UI                         |
| `/api/state`、`/api/keys*` 等     | HTTP Basic                    | 管理 JSON API                       |
| `/v1`、`/v1/*`                    | Bearer (`PROXY_TOKEN`)        | OpenAI 兼容代理                     |
| `/healthz`                        | 无                            | 存活检查，返回 `ok`                 |

后台用浏览器原生 Basic Auth（省得维护登录页），`/v1` 用 Bearer（方便 SDK 填）。

---

## 安全性

### 自测扫描

部署好后推荐跑一遍自测脚本，模拟 Shodan / Censys / nuclei 这类公网扫描器
的探测行为：

```bash
./scripts/abuse-scan.sh https://gateway.example.com
```

会跑六组检查：指纹暴露面、常见路径枚举、Bearer 暴力破解吞吐、Basic Auth
暴力破解、nuclei 规则扫描、本地密钥卫生。缺失的可选工具（`ffuf` / `hydra` /
`nuclei` / `gitleaks`）会 SKIP 并给装包提示，不影响其他项。

### 内置防御

- **按 IP 失败认证限流**：60 秒内达到 `AUTH_FAIL_LIMIT`（默认 10）次失败后，
  该 IP 被封禁 `AUTH_BLOCK_MINUTES`（默认 15）分钟，返回 `429 + Retry-After`。
- **启动期凭证长度校验**：`ADMIN_PASSWORD < 12` 或 `PROXY_TOKEN < 24` 直接拒绝启动。
- **通用 Basic Auth realm**（`"Restricted"`，不是产品名），不让 Shodan 按关键词搜到。
- **`/healthz` 是唯一未认证端点**，只返回字面量 `ok`，不泄漏其他信息。
- **`X-Forwarded-For` 感知**，限流器识别反代传来的真实客户端 IP。

### 配合 fail2ban（可选，增强）

每次失败认证都会在容器日志里输出一条 `auth_fail` 行，格式：

```
2026/04/24 09:12:04 auth_fail ip=203.0.113.7 path=/api/state ua="curl/8.1" scheme=basic
```

把下面三段粘进去：

```bash
# 过滤器
sudo tee /etc/fail2ban/filter.d/ai-gateway.conf >/dev/null <<'EOF'
[Definition]
failregex = ^.*auth_fail ip=<HOST> .*$
EOF

# 定位容器日志路径
CID=$(docker inspect -f '{{.Id}}' ai-gateway-poller)
LOG=/var/lib/docker/containers/$CID/$CID-json.log

# jail
sudo tee /etc/fail2ban/jail.d/ai-gateway.conf >/dev/null <<EOF
[ai-gateway]
enabled  = true
filter   = ai-gateway
logpath  = $LOG
maxretry = 20
findtime = 600
bantime  = 86400
EOF

sudo systemctl restart fail2ban
```

这样应用层 429 + 操作系统层 iptables 丢包叠加，重复违规的 IP 一整天连
TCP 都接不上。

### 故意没做的部分

- **IP 白名单 / mTLS**：留给反代配（Caddy `@allowed { remote_ip ... }` /
  Nginx `allow / deny`），应用层不掺和。
- **隐藏 `/healthz`**：没必要，只回 `ok`，并且 Docker 健康检查也需要它。
- **把后台 UI 放随机路径**：纯障眼法，扫描器看到 `/` 是 401 就走了，
  换路径不会换来实质安全。

---

## 不用 Docker 的本地开发

```bash
export ADMIN_USER=admin
export ADMIN_PASSWORD=localdev-password-123    # ≥ 12 位
export PROXY_TOKEN=$(openssl rand -hex 32)

go run .
# 浏览器打开 http://127.0.0.1:9090/
```

---

## 常见问题

**Q: `docker compose up` 报端口 9090 被占用**
A: 别的服务占了。改 `docker-compose.yml` 里的 `127.0.0.1:9090:9090` 左边那个
端口号（比如 `9190`），重新 `docker compose up -d`，然后 Caddy/Nginx 也指向新端口。

**Q: 后台添加了 Key 但 `/v1` 报 "no available key"**
A: 进后台看 Key 是不是被暂停了（月消耗超过 `MONTHLY_COOLDOWN_USD`）。暂停的
Key 下个月 1 号会自动解锁，也可以在后台手动重置。

**Q: 怎么知道自己的 PROXY_TOKEN？**
A: `cat credentials.txt` 或 `grep PROXY_TOKEN .env`。

**Q: 密码忘了**
A: 重跑 `bash install.sh`，选覆盖。数据卷里的 Key 不会丢。

**Q: 怎么轮换 PROXY_TOKEN**
A: 重跑 `bash install.sh`；`ADMIN_USER` / `ADMIN_PASSWORD` 输入原来的值，
脚本会自动生成新的 `PROXY_TOKEN`。别忘了更新所有 SDK 客户端里的 `api_key`。
