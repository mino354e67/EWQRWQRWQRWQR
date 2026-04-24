# AI Gateway 密钥轮询器

自部署的 [Vercel AI Gateway](https://vercel.com/ai-gateway) 密钥池代理：
管理一组 Gateway API Key、跟踪每个 Key 的月度消耗、在达到设定阈值后自动
暂停该 Key，并对外暴露一个 OpenAI 兼容的 `/v1` 端点。每次请求会挑选当前
花费最少且未暂停的 Key，透明转发到上游 AI Gateway。

---

## 一键部署（Debian / Ubuntu VPS）

前提：一台全新的 Debian 11+/Ubuntu 20.04+ VPS，有一个已解析到本机公网 IP
的域名，以 root（或 sudo 用户）登录。下面四步按顺序粘贴即可。

### 步骤 1 —— 装 Docker、Caddy 及工具链

新机器完整粘贴；已经装过的可以跳过。

```bash
sudo apt update
sudo apt install -y git curl openssl ca-certificates gnupg debian-keyring debian-archive-keyring apt-transport-https

# Docker 官方源
sudo install -m 0755 -d /etc/apt/keyrings
curl -fsSL https://download.docker.com/linux/$(. /etc/os-release; echo "$ID")/gpg \
  | sudo gpg --dearmor -o /etc/apt/keyrings/docker.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/$(. /etc/os-release; echo "$ID") $(. /etc/os-release; echo "$VERSION_CODENAME") stable" \
  | sudo tee /etc/apt/sources.list.d/docker.list >/dev/null

# Caddy 官方源
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
  | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
  | sudo tee /etc/apt/sources.list.d/caddy-stable.list >/dev/null

sudo apt update
sudo apt install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin caddy
sudo systemctl enable --now docker caddy
```

### 步骤 2 —— 启动服务

**修改下面最上面的三行**，然后整块粘贴。其余行完全照抄。

```bash
# ↓↓↓ 只改这三行 ↓↓↓
export DOMAIN=gateway.example.com                    # 你的域名
export ADMIN_USER=admin                              # 管理后台用户名
export ADMIN_PASSWORD='Change-This-To-Strong-Pass'   # 至少 12 位，建议包含大小写+数字+符号
# ↑↑↑ 只改这三行 ↑↑↑

cd ~ && git clone https://github.com/mino354e67/ewqrwqrwqrwqr.git ai-gateway-poller
cd ai-gateway-poller
PROXY_TOKEN=$(openssl rand -hex 32)
# 用 printf 而不是 heredoc，避免密码里包含 $ 或反引号时被二次展开
printf 'ADMIN_USER=%s\nADMIN_PASSWORD=%s\nPROXY_TOKEN=%s\n' \
  "$ADMIN_USER" "$ADMIN_PASSWORD" "$PROXY_TOKEN" > .env
chmod 600 .env
sudo docker compose up -d --build

sudo tee /etc/caddy/Caddyfile >/dev/null <<EOF
$DOMAIN {
    reverse_proxy 127.0.0.1:9090
}
EOF
sudo systemctl reload caddy

echo
echo "=================================================="
echo "  部署完成，请妥善保存以下凭证："
echo "    域名        : https://$DOMAIN"
echo "    管理员用户  : $ADMIN_USER"
echo "    管理员密码  : $ADMIN_PASSWORD"
echo "    代理 Bearer : $PROXY_TOKEN"
echo "=================================================="
echo
echo "接下来："
echo "  1) 打开浏览器访问 https://$DOMAIN/，输入上面的用户名/密码登录管理后台"
echo "  2) 在后台添加你的 Vercel AI Gateway API Key"
echo "  3) 在你的 AI SDK 里把 base_url 设为 https://$DOMAIN/v1 ，api_key 填上面的 Bearer"
```

`.env` 已写入当前目录并被仓库的 `.gitignore` 排除，不会被意外 `git push`
出去。PROXY_TOKEN 只在部署时展示一次，忘了就看 `cat .env`。

### 步骤 3 —— 验证（可选）

```bash
# 健康检查（应输出 ok）
curl -s "https://$DOMAIN/healthz"; echo

# 认证头指纹（应看到 realm="Restricted"）
curl -sI "https://$DOMAIN/" | grep -i www-authenticate

# 跑一遍自测脚本
./scripts/abuse-scan.sh "https://$DOMAIN"
```

### 步骤 4 —— 在 SDK 中使用

Python（把 `$DOMAIN` 和 `$PROXY_TOKEN` 替换成步骤 2 输出的值；如果就在刚才
那个终端里，环境变量还在，可以直接跑）：

```python
from openai import OpenAI
import os

client = OpenAI(
    base_url=f"https://{os.environ['DOMAIN']}/v1",
    api_key=os.environ["PROXY_TOKEN"],
)
resp = client.chat.completions.create(
    model="anthropic/claude-sonnet-4.5",
    messages=[{"role": "user", "content": "你好"}],
)
print(resp.choices[0].message.content)
```

Claude SDK / Cursor / Cline / LobeChat 等任何填得了 `base_url`（或叫
"OpenAI Compatible Endpoint"）和 `api_key` 两栏的客户端，都照这个格式填
就行。

---

## 运维快捷命令

```bash
cd ~/ai-gateway-poller

# 查看日志（含 auth_fail 失败认证记录）
sudo docker compose logs -f app

# 重启
sudo docker compose restart app

# 更新到最新代码
git pull && sudo docker compose up -d --build

# 轮换 PROXY_TOKEN（记得同步到所有客户端）
NEW_TOKEN=$(openssl rand -hex 32)
sed -i.bak "s|^PROXY_TOKEN=.*|PROXY_TOKEN=$NEW_TOKEN|" .env
sudo docker compose up -d
echo "新 token: $NEW_TOKEN"

# 改管理员密码
read -s -p "新密码（≥12 位）: " NEW_PW; echo
sed -i.bak "s|^ADMIN_PASSWORD=.*|ADMIN_PASSWORD=$NEW_PW|" .env
sudo docker compose up -d

# 停止（保留数据卷）
sudo docker compose down

# 彻底清空（⚠️ 会丢失所有已保存的 Gateway Key）
sudo docker compose down -v
```

---

## 端点与环境变量

### 端点

| 路径                                                       | 认证                                        | 用途                               |
| ---------------------------------------------------------- | ------------------------------------------- | ---------------------------------- |
| `/`、`/index.html`                                         | HTTP Basic（`ADMIN_USER`/`ADMIN_PASSWORD`） | 管理后台 Web UI                    |
| `/api/state`、`/api/keys`、`/api/keys/<id>`、`/api/refresh` | HTTP Basic                                  | 管理后台 JSON API                  |
| `/v1`、`/v1/*`                                             | Bearer Token（`PROXY_TOKEN`）               | OpenAI 兼容代理，转发至 AI Gateway |
| `/healthz`                                                 | 无                                          | 存活检查，始终返回 `ok`            |

**为什么用两套认证？** 管理后台复用浏览器原生的 Basic Auth 弹框，省去维护
登录页的麻烦；而大多数 AI SDK 客户端只能填一个 API Key，所以 `/v1` 改用
Bearer Token。

### 环境变量

| 变量                   | 是否必填 | 默认值                                                        |
| ---------------------- | -------- | ------------------------------------------------------------- |
| `ADMIN_USER`           | 是       | —                                                             |
| `ADMIN_PASSWORD`       | 是       | —（至少 12 位）                                               |
| `PROXY_TOKEN`          | 是       | —（至少 24 位，建议 `openssl rand -hex 32`）                  |
| `LISTEN_ADDR`          | 否       | `:9090`                                                       |
| `STATE_DIR`            | 否       | `.`（容器内默认 `/data`）                                     |
| `GATEWAY_BASE_URL`     | 否       | `https://ai-gateway.vercel.sh/v1`                             |
| `MONTHLY_COOLDOWN_USD` | 否       | `5`                                                           |
| `AUTH_FAIL_LIMIT`      | 否       | `10`（60 秒内的失败次数阈值，设 `0` 关闭限流）                |
| `AUTH_BLOCK_MINUTES`   | 否       | `15`（触发后的封禁时长）                                      |

必填变量任一缺失或长度不达标时，进程会立即退出并打印错误 —— 这是故意的，
避免公网部署时裸奔弱凭证。

---

## 不用 Docker 的本地开发

```bash
export ADMIN_USER=admin
export ADMIN_PASSWORD=localdev-password      # 至少 12 位
export PROXY_TOKEN=$(openssl rand -hex 32)   # 32 位十六进制够了

go run .
# 浏览器打开 http://127.0.0.1:9090
```

---

## 安全性：自测与内置防御

部署到公网 VPS 之后，这类服务必然会被 Shodan、Censys、nuclei 等自动化
扫描器扫到。仓库里自带一个脚本，模拟这些扫描器的行为，让你自己复核一遍：

```bash
./scripts/abuse-scan.sh "https://$DOMAIN"
```

脚本一共跑六组检查：

1. **指纹暴露面** —— 响应头、realm 字符串、`Server`/`X-Powered-By` 等泄漏。
2. **常见路径枚举** —— `/admin`、`/.git/config`、`/.env`、`/phpmyadmin`
   等几十个热门路径，确认全部返回 401/404 而非 200。
3. **Bearer 暴力破解吞吐** —— 连续打 100 次错误 Token，看限流是否生效。
4. **Basic Auth 暴力破解** —— 有 `hydra` 的话跑一把常见弱密码字典。
5. **nuclei 模板扫描** —— exposures + misconfiguration 类规则。
6. **本地密钥卫生** —— 检查 `.env` 是否被 gitignore、git 历史里是否写入
   过 `PROXY_TOKEN=` 之类字样，有 `gitleaks` 就顺便跑一下。

缺失的可选工具（`ffuf`、`hydra`、`nuclei`、`gitleaks`）会输出 SKIP 并
给出安装提示，不会影响其他检查。任何一项 FAIL 脚本都会以非 0 退出，可以
挂到 CI/定时任务里。

### 已经内置的防御

- **按 IP 的失败认证限流**：在 60 秒内达到 `AUTH_FAIL_LIMIT`（默认 10）次
  错误认证后，该 IP 在 `AUTH_BLOCK_MINUTES`（默认 15）分钟内一律返回
  `429 Too Many Requests`（带 `Retry-After` 头），`/api/*` 和 `/v1/*` 都
  生效。紧急情况下设 `AUTH_FAIL_LIMIT=0` 关闭限流。
- **凭证长度强制**：启动时拒绝 `ADMIN_PASSWORD < 12` 或 `PROXY_TOKEN < 24`。
- **通用 Basic Auth realm**：对外宣告 `realm="Restricted"`，而不是
  "AI Gateway Admin" 这种有辨识度的字符串 —— Shodan/Censys 按 realm 搜索
  时，你的部署会淹没在上百万个通用 realm 响应里。
- **`/healthz`** 是唯一未认证端点，只返回字面量 `ok`，不泄漏其他信息。
- **`X-Forwarded-For` 感知**：限流器识别 Caddy/Nginx 传来的真实客户端 IP，
  而不是把所有请求当成同一个反代 IP。

### 配合 fail2ban（可选，增强封禁）

每一次失败认证都会写一条前缀为 `auth_fail` 的日志：

```
2026/04/24 09:12:04 auth_fail ip=203.0.113.7 path=/api/state ua="curl/8.1" scheme=basic
```

复制粘贴以下三段：

```bash
# 过滤器
sudo tee /etc/fail2ban/filter.d/ai-gateway.conf >/dev/null <<'EOF'
[Definition]
failregex = ^.*auth_fail ip=<HOST> .*$
EOF

# 找到你容器的日志路径
CID=$(sudo docker inspect -f '{{.Id}}' ai-gateway-poller)
LOG=/var/lib/docker/containers/$CID/$CID-json.log

# jail 配置
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

这样：应用层 429 叠加操作系统层的 iptables 丢包，重复违规的 IP 会被整机
封禁一天。

### 故意没有做的部分

- **针对已知域名的定向攻击**：程序层不做 mTLS 或 IP 白名单 —— 真要用的话，
  在反代里配（Caddy：`@allowed { remote_ip ... }`；Nginx：`allow/deny`）。
- **`PROXY_TOKEN` 被客户端意外上传**：如果 SDK 客户端代码仓库把 Token
  提交到了公共 GitHub，这个 Token 就算泄漏了。按上面的"轮换 PROXY_TOKEN"
  块直接改并重启即可。
- **混淆管理后台路径**：不把 UI 藏到随机前缀下，因为扫描器看到 `/` 返回
  401 会直接走人，换路径反而增加运维成本却换不来实质安全收益。
- **隐藏 `/healthz`**：它只回一句 `ok`，目标攻击者从 DNS/SNI 已经知道服务
  存在，没必要再加一层密钥（Docker 健康检查也还要用它）。

---

## 运行原理简图

```
   OpenAI 兼容 SDK                                        Vercel AI Gateway
  (Authorization:   ───▶  /v1/*  ───▶  限流+鉴权  ───▶  选花费最低的     ───▶  https://ai-gateway.vercel.sh/v1
   Bearer $PROXY_TOKEN)               (PROXY_TOKEN)   未暂停的 Key           (Authorization: Bearer <该 Key>)

   浏览器           ───▶  /、/api/*  ───▶  限流+鉴权  ───▶  state.json (持久化在卷里)
  (Basic Auth)                        (ADMIN_USER/PW)
```

## 项目结构

```
.
├── main.go              # HTTP 服务器 + 状态机 + 代理核心
├── ratelimit.go         # 按 IP 滑窗限流器 + X-Forwarded-For 解析
├── index.html           # 管理后台单页应用（go:embed 内联）
├── Dockerfile           # 多阶段构建：golang:1.24 -> alpine:3.20
├── docker-compose.yml   # 生产部署编排，带 healthcheck 和命名卷
├── .env.example         # 环境变量模板
├── scripts/
│   └── abuse-scan.sh    # 对外暴露面自测脚本
├── CLAUDE.md            # 给 Claude Code 的项目约定
└── README.md
```
