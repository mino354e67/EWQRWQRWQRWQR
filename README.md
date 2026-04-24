# AI Gateway 密钥轮询器

自部署的 [Vercel AI Gateway](https://vercel.com/ai-gateway) 密钥池代理：
管理一组 Gateway API Key、跟踪每个 Key 的月度消耗、在达到设定阈值后自动
暂停该 Key，并对外暴露一个 OpenAI 兼容的 `/v1` 端点。每次请求会挑选当前
花费最少且未暂停的 Key，透明转发到上游 AI Gateway。

## 端点

| 路径                                                       | 认证                                        | 用途                                  |
| ---------------------------------------------------------- | ------------------------------------------- | ------------------------------------- |
| `/`、`/index.html`                                         | HTTP Basic（`ADMIN_USER`/`ADMIN_PASSWORD`） | 管理后台 Web UI                       |
| `/api/state`、`/api/keys`、`/api/keys/<id>`、`/api/refresh` | HTTP Basic                                  | 管理后台 JSON API                     |
| `/v1`、`/v1/*`                                             | Bearer Token（`PROXY_TOKEN`）               | OpenAI 兼容代理，转发至 AI Gateway    |
| `/healthz`                                                 | 无                                          | 存活检查，始终返回 `ok`               |

**为什么用两套认证？** 管理后台复用浏览器原生的 Basic Auth 弹框，省去维护
登录页的麻烦。但大多数 AI SDK 客户端只能填一个 API Key，所以 `/v1` 改用
Bearer Token —— 在 SDK 中把 `base_url` 设为 `http://你的域名:9090/v1`、
`api_key` 设为 `$PROXY_TOKEN` 即可。

## 环境变量

| 变量                   | 是否必填 | 默认值                                    |
| ---------------------- | -------- | ----------------------------------------- |
| `ADMIN_USER`           | 是       | —                                         |
| `ADMIN_PASSWORD`       | 是       | —（至少 12 位）                           |
| `PROXY_TOKEN`          | 是       | —（至少 24 位，建议 `openssl rand -hex 32`） |
| `LISTEN_ADDR`          | 否       | `:9090`                                   |
| `STATE_DIR`            | 否       | `.`（容器内默认 `/data`）                 |
| `GATEWAY_BASE_URL`     | 否       | `https://ai-gateway.vercel.sh/v1`         |
| `MONTHLY_COOLDOWN_USD` | 否       | `5`                                       |
| `AUTH_FAIL_LIMIT`      | 否       | `10`（失败次数阈值，`0` 关闭限流）        |
| `AUTH_BLOCK_MINUTES`   | 否       | `15`（触发后的封禁时长）                  |

必填变量任一缺失时进程会立即退出并打印错误 —— 这是故意的，避免在公网 VPS 上
裸奔时泄漏已保存的 Gateway Key。

## 使用 Docker Compose 部署

```bash
git clone <本仓库> ai-gateway-poller
cd ai-gateway-poller

cp .env.example .env
# 编辑 .env：设置 ADMIN_USER、ADMIN_PASSWORD，以及一个足够长的随机 PROXY_TOKEN
#   PROXY_TOKEN=$(openssl rand -hex 32)

docker compose up -d --build
docker compose logs -f app
```

默认 `docker-compose.yml` 只把端口绑在 `127.0.0.1:9090`，需要你用反向代理
（Caddy、Nginx、Traefik 等）套一层 HTTPS 后再对外暴露。状态文件
`state.json`（包含你的 Gateway API Key）持久化在命名卷 `gateway-state` 中。

### Caddy 反代示例

```caddy
gateway.example.com {
    reverse_proxy 127.0.0.1:9090
}
```

Caddy 会自动处理证书签发与续签，这一段就够了。

### 运维操作

```bash
# 重启
docker compose restart app

# 查看日志（含 auth_fail 失败记录）
docker compose logs -f app

# 停止并删除容器（卷会保留，Key 不会丢）
docker compose down

# 连卷一起删（慎用，会丢失所有已保存的 Key）
docker compose down -v

# 轮换 PROXY_TOKEN：编辑 .env 后
docker compose up -d
```

## 本地开发（不用 Docker）

```bash
export ADMIN_USER=admin
export ADMIN_PASSWORD=localdev-password      # 至少 12 位
export PROXY_TOKEN=$(openssl rand -hex 32)   # 32 位十六进制足够

go run .
# 浏览器打开 http://127.0.0.1:9090，会弹出管理后台账号/密码输入框
```

如果 `ADMIN_PASSWORD` 少于 12 位、或 `PROXY_TOKEN` 少于 24 位，程序会拒绝
启动。这是为了避免线上裸跑弱密码。

## 在 OpenAI 兼容 SDK 中使用

```python
from openai import OpenAI

client = OpenAI(
    base_url="https://gateway.example.com/v1",
    api_key="<你的 PROXY_TOKEN>",
)
resp = client.chat.completions.create(
    model="anthropic/claude-sonnet-4.5",
    messages=[{"role": "user", "content": "你好"}],
)
```

代理收到请求后会剥掉你发来的 `PROXY_TOKEN` 头，换成池子里选中的那把
Gateway Key 再转发上游 —— 你的 Gateway Key 永远不会离开服务器。

## 安全性：自测与内置防御

部署到公网 VPS 之后，这类服务必然会被 Shodan、Censys、nuclei 自动化扫描器
扫到。仓库里自带一个脚本，模拟这些扫描器的行为，让你自己验证一遍：

```bash
./scripts/abuse-scan.sh https://gateway.example.com
```

脚本一共跑六组检查：

1. **指纹暴露面** —— 响应头、realm 字符串、`Server`/`X-Powered-By` 等泄漏。
2. **常见路径枚举** —— `/admin`、`/.git/config`、`/.env`、`/phpmyadmin` 等
   几十个热门路径，确认全部返回 401/404 而非 200。
3. **Bearer 暴力破解吞吐** —— 连续打 100 次错误 Token，看限流是否生效。
4. **Basic Auth 暴力破解** —— 有 `hydra` 的话跑一把常见弱密码字典。
5. **nuclei 模板扫描** —— exposures + misconfiguration 类规则。
6. **本地密钥卫生** —— 检查 `.env` 是否被 gitignore、git 历史里是否写入过
   `PROXY_TOKEN=` 之类字样，有 `gitleaks` 就顺便跑一下。

缺失的可选工具（`ffuf`、`hydra`、`nuclei`、`gitleaks`）会输出 SKIP 并给出
安装提示，不会影响其他检查。任何一项 FAIL 脚本都会以非 0 退出，可以挂到
CI/定时任务里。

### 已经内置的防御

- **按 IP 的失败认证限流**：在 60 秒内达到 `AUTH_FAIL_LIMIT`（默认 10）次
  错误认证后，该 IP 在 `AUTH_BLOCK_MINUTES`（默认 15）分钟内一律返回
  `429 Too Many Requests`（带 `Retry-After` 头），`/api/*` 和 `/v1/*` 都生效。
  紧急情况下可设 `AUTH_FAIL_LIMIT=0` 关闭限流。
- **凭证长度强制**：启动时拒绝 `ADMIN_PASSWORD < 12` 或 `PROXY_TOKEN < 24`。
- **通用 Basic Auth realm**：对外宣告的是 `realm="Restricted"`，而不是
  「AI Gateway Admin」这种有辨识度的字符串。Shodan/Censys 用 realm 做关键词
  搜索时，你的部署会淹没在上百万个通用 realm 的 401 响应里。
- **`/healthz` 是唯一未认证端点**，只会返回字面量 `ok`，不泄漏任何其他信息。
- **`X-Forwarded-For` 感知**：限流器识别反代传来的真实客户端 IP，而不是把
  所有请求当成同一个反代 IP。

### 配合 fail2ban

每一次失败认证都会写一条前缀为 `auth_fail` 的日志：

```
2026/04/24 09:12:04 auth_fail ip=203.0.113.7 path=/api/state ua="curl/8.1" scheme=basic
```

把 fail2ban 指向 Docker 日志流（或你反代的 access log，前提是反代也记录了
`X-Forwarded-For`），过滤器示例：

```ini
# /etc/fail2ban/filter.d/ai-gateway.conf
[Definition]
failregex = ^.*auth_fail ip=<HOST> .*$
```

然后写一个 jail 读取容器日志（`/var/lib/docker/containers/<id>/*-json.log`
或 `journalctl -u docker`）。效果：应用层 429 叠加操作系统层的直接丢包。

### 故意没有做的部分

- **针对已知域名的定向攻击**：程序层不做 mTLS 或 IP 白名单 —— 真要用的话，
  在反代里配（Caddy：`@allowed { remote_ip ... }`；Nginx：`allow/deny`）。
- **`PROXY_TOKEN` 被客户端意外上传**：如果你的 SDK 客户端代码仓库不小心把
  Token 提交到了公共 GitHub，这个 Token 就算泄漏了。轮换方式：改 `.env`
  然后 `docker compose up -d`，同时更新所有 SDK 客户端里的 key 填写。
- **混淆管理后台路径**：不把 UI 藏到随机前缀下，因为扫描器看到 `/` 就是
  401 会直接走人，换路径反而增加运维成本却换不来实质安全收益。
- **隐藏 `/healthz`**：它只回一句 `ok`，目标攻击者从 DNS/SNI 已经知道服务
  存在，没必要再加一层密钥。

## 运行原理简图

```
   OpenAI SDK                                        Vercel AI Gateway
  (Authorization:  ───▶  /v1/*  ───▶  限流+鉴权  ───▶  选花费最低的       ───▶  https://ai-gateway.vercel.sh/v1
   Bearer $PROXY_TOKEN)             (PROXY_TOKEN)   未暂停的 Key            (Authorization: Bearer <该 Key>)

   浏览器         ───▶  /、/api/*  ───▶  限流+鉴权  ───▶  state.json (持久化在卷里)
  (Basic Auth)                      (ADMIN_USER/PW)
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
└── README.md
```
