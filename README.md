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
export ADMIN_PASSWORD=localdev-password  # must be >= 12 chars
export PROXY_TOKEN=$(openssl rand -hex 32)

go run .
# open http://127.0.0.1:9090 — browser will prompt for the admin credentials
```

The service refuses to start if `ADMIN_PASSWORD` is shorter than 12 characters
or `PROXY_TOKEN` is shorter than 24 characters, to prevent silent deployment
with weak credentials.

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

## Security posture & testing

Once deployed on a public VPS, the service is fingerprintable by internet-wide
scanners (Shodan, Censys, nuclei-driven bots). To verify nothing exploitable
is exposed, run the included scan script against your own deployment:

```bash
./scripts/abuse-scan.sh https://gateway.example.com
```

The script performs six checks: fingerprint surface, common-path enumeration,
Bearer brute-force productivity, Basic-auth brute-force productivity, a nuclei
exposures scan, and local git/secret hygiene. Optional tools (`ffuf`, `hydra`,
`nuclei`, `gitleaks`) are used if installed, or the corresponding check SKIPs
with install hints. The script exits non-zero on any FAIL, so it can be wired
into CI.

### Built-in defenses

- **Per-IP failed-auth rate limit**: after `AUTH_FAIL_LIMIT` (default 10) bad
  credentials in 60 seconds, the client's IP is blocked for
  `AUTH_BLOCK_MINUTES` (default 15) on both `/api/*` and `/v1/*`. The block
  response is `429` with a `Retry-After` header. Set `AUTH_FAIL_LIMIT=0` to
  disable in emergencies.
- **Entropy enforcement**: startup refuses to run with `ADMIN_PASSWORD < 12`
  chars or `PROXY_TOKEN < 24` chars.
- **Generic realm**: Basic Auth advertises `realm="Restricted"` rather than
  any product-identifying string, so Shodan/Censys searches don't surface this
  deployment to anyone grepping for the software by name.
- **`/healthz`** is the only unauthenticated endpoint; it returns a literal
  `ok` and leaks no other information.

### fail2ban integration

Every failed attempt is logged as a single-line record prefixed with
`auth_fail `, e.g.:

```
2026/04/24 09:12:04 auth_fail ip=203.0.113.7 path=/api/state ua="curl/8.1" scheme=basic
```

Point fail2ban at the Docker log stream (or your reverse-proxy access log if
you're propagating `X-Forwarded-For` in) with a filter like:

```ini
# /etc/fail2ban/filter.d/ai-gateway.conf
[Definition]
failregex = ^.*auth_fail ip=<HOST> .*$
```

and a jail that reads the container's log file or `journalctl -u docker`.
This turns repeated 429s into OS-level packet drops.

### What's intentionally NOT protected

- **Targeted attacker with the correct domain name**: there's no mTLS or
  IP allowlist in-app. If you need those, configure them in your reverse
  proxy (Caddy: `@allowed { remote_ip ... }`, Nginx: `allow/deny`).
- **Supply-chain leak of `PROXY_TOKEN`**: if a client app accidentally
  commits it to a public repo, the token is compromised. Rotate by editing
  `.env` and `docker compose up -d`. SDK clients must be updated.
