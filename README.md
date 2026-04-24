# AI Gateway Poller

Self-hosted pool/proxy in front of [Vercel AI Gateway](https://vercel.com/ai-gateway).
Stores a set of Gateway API keys, tracks each one's monthly usage, pauses keys
that hit a configurable cooldown, and exposes a single `/v1` endpoint that
OpenAI-compatible SDKs can call — the proxy picks the cheapest unpaused key on
every request and transparently forwards the call upstream.

## Endpoints

| Path                    | Auth                          | Purpose                                     |
| ----------------------- | ----------------------------- | ------------------------------------------- |
| `/`, `/index.html`      | HTTP Basic (`ADMIN_USER`/`ADMIN_PASSWORD`) | Admin web UI                    |
| `/api/state`, `/api/keys`, `/api/keys/<id>`, `/api/refresh` | HTTP Basic | Admin JSON API         |
| `/v1`, `/v1/*`          | Bearer token (`PROXY_TOKEN`)  | OpenAI-compatible proxy to the AI Gateway   |
| `/healthz`              | None                          | Liveness probe (always returns `ok`)        |

Why two different schemes? The admin panel uses the browser's native Basic Auth
prompt so there's no login page to maintain. Most AI SDK clients, however, only
let you fill in a single API key, so `/v1` uses a Bearer token instead —
configure your SDK with `base_url = http://your-host:9090/v1` and
`api_key = $PROXY_TOKEN`.

## Environment variables

| Variable               | Required | Default                                    |
| ---------------------- | -------- | ------------------------------------------ |
| `ADMIN_USER`           | yes      | —                                          |
| `ADMIN_PASSWORD`       | yes      | —                                          |
| `PROXY_TOKEN`          | yes      | —                                          |
| `LISTEN_ADDR`          | no       | `:9090`                                    |
| `STATE_DIR`            | no       | `.` (container default: `/data`)           |
| `GATEWAY_BASE_URL`     | no       | `https://ai-gateway.vercel.sh/v1`          |
| `MONTHLY_COOLDOWN_USD` | no       | `5`                                        |

If any of the required variables are unset the process exits immediately with
a clear error — this is intentional, since running without auth on a public
VPS would leak every stored Gateway key.

## Deploy with Docker Compose

```bash
git clone <this-repo> ai-gateway-poller
cd ai-gateway-poller

cp .env.example .env
# edit .env: set ADMIN_USER, ADMIN_PASSWORD, and a long random PROXY_TOKEN
#   PROXY_TOKEN=$(openssl rand -hex 32)

docker compose up -d --build
docker compose logs -f app
```

By default `docker-compose.yml` binds the service to `127.0.0.1:9090` only.
Put a reverse proxy (Caddy, Nginx, Traefik, …) in front of it to add TLS and
expose it publicly. State (`state.json`, which stores your Gateway API keys) is
persisted in the named volume `gateway-state`.

### Example: Caddy reverse proxy

```caddy
gateway.example.com {
    reverse_proxy 127.0.0.1:9090
}
```

That's all that's needed — Caddy handles the certificate automatically.

## Local development (no Docker)

```bash
export ADMIN_USER=admin
export ADMIN_PASSWORD=hunter2
export PROXY_TOKEN=dev-token

go run .
# open http://127.0.0.1:9090 — browser will prompt for the admin credentials
```

## Using the proxy from an OpenAI-compatible SDK

```python
from openai import OpenAI

client = OpenAI(
    base_url="https://gateway.example.com/v1",
    api_key="<your PROXY_TOKEN>",
)
resp = client.chat.completions.create(
    model="anthropic/claude-sonnet-4.5",
    messages=[{"role": "user", "content": "hello"}],
)
```

The proxy strips your `PROXY_TOKEN` header and replaces it with one of the
stored Gateway API keys before forwarding upstream, so your pool keys never
leave the server.
