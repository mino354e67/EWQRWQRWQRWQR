#!/usr/bin/env bash
# abuse-scan.sh — simulate the scans that internet-wide bots run against
# this service, so you can verify nothing exploitable is exposed.
#
# Usage:
#   ./scripts/abuse-scan.sh https://gateway.example.com
#   ./scripts/abuse-scan.sh http://127.0.0.1:9090         # local dev
#
# Exits 0 on all-pass, 1 if any check fails. Missing optional tools produce
# SKIP (not FAIL) with install hints.

set -u

URL="${1:-}"
if [[ -z "$URL" ]]; then
  echo "usage: $0 <base-url>" >&2
  exit 2
fi
URL="${URL%/}"

PASS=0; FAIL=0; WARN=0; SKIP=0
RESULTS=()

record() { RESULTS+=("$1"); case "$1" in PASS*) PASS=$((PASS+1));; FAIL*) FAIL=$((FAIL+1));; WARN*) WARN=$((WARN+1));; SKIP*) SKIP=$((SKIP+1));; esac; }

hr() { printf '%s\n' "------------------------------------------------------------"; }
hdr() { hr; printf '== %s\n' "$1"; hr; }

# -------- 1. Fingerprint surface --------
hdr "1. Fingerprint surface"

hdrs_root=$(curl -sI "$URL/" || true)
hdrs_healthz=$(curl -sI "$URL/healthz" || true)
hdrs_v1=$(curl -sI -X POST "$URL/v1/models" || true)
body_healthz=$(curl -s "$URL/healthz" || true)

status_of() { awk 'NR==1{print $2}' <<<"$1"; }

if [[ "$(status_of "$hdrs_healthz")" == "200" && "$body_healthz" == "ok" ]]; then
  echo "  healthz: 200 ok"
  record "PASS healthz_200"
else
  echo "  healthz: UNEXPECTED"
  echo "$hdrs_healthz" | sed 's/^/    /'
  record "FAIL healthz"
fi

root_status=$(status_of "$hdrs_root")
root_realm=$(awk 'tolower($1)=="www-authenticate:"{$1=""; sub(/^ /,""); print}' <<<"$hdrs_root" | tr -d '\r')
if [[ "$root_status" == "401" && "$root_realm" =~ [Bb]asic[[:space:]]+realm=\"(.+)\" ]]; then
  realm="${BASH_REMATCH[1]}"
  echo "  /: 401 realm=\"$realm\""
  if [[ "$realm" == "Restricted" ]]; then
    record "PASS root_realm_generic"
  else
    echo "  WARN: realm \"$realm\" is a unique fingerprint — Shodan/Censys index it."
    echo "        Try:  shodan search 'http.headers.www_authenticate:\"$realm\"'"
    record "WARN root_realm_distinctive"
  fi
else
  echo "  /: UNEXPECTED"
  echo "$hdrs_root" | sed 's/^/    /'
  record "FAIL root_unexpected"
fi

v1_status=$(status_of "$hdrs_v1")
if [[ "$v1_status" == "401" ]] && ! grep -qi '^www-authenticate:' <<<"$hdrs_v1"; then
  echo "  /v1/models: 401 (no realm leak, good)"
  record "PASS v1_bearer_only"
else
  echo "  /v1/models: status=$v1_status (may leak realm)"
  echo "$hdrs_v1" | sed 's/^/    /'
  record "FAIL v1_header"
fi

for leak in "^Server: " "^X-Powered-By:" "^X-AspNet-Version:"; do
  if grep -Eqi "$leak" <<<"$hdrs_root$hdrs_v1"; then
    echo "  WARN: leaking header matching /$leak/"
    record "WARN header_leak"
  fi
done

# -------- 2. Path enumeration --------
hdr "2. Path enumeration"

PATHS=(admin administrator wp-admin wp-login.php .git/config .env
       config.json api/v1 api/users graphql actuator server-status
       phpmyadmin console debug metrics prometheus swagger openapi.json
       docs _next server .aws/credentials id_rsa backup.sql dump.sql)

hits=0
if command -v ffuf >/dev/null 2>&1; then
  echo "  using ffuf"
  wl=$(mktemp); printf '%s\n' "${PATHS[@]}" > "$wl"
  ffuf_out=$(ffuf -u "$URL/FUZZ" -w "$wl" -mc 200,301,302,403 -s -of csv -o /tmp/ffuf.csv 2>/dev/null || true)
  rm -f "$wl"
  hits=$(tail -n +2 /tmp/ffuf.csv 2>/dev/null | wc -l | tr -d ' ')
  if [[ "$hits" == "0" ]]; then
    echo "  0 unexpected hits across ${#PATHS[@]} paths"
    record "PASS path_enum"
  else
    echo "  $hits paths responded 200/301/302/403:"
    tail -n +2 /tmp/ffuf.csv | awk -F, '{print "    " $2 " -> " $5}'
    record "FAIL path_enum"
  fi
  rm -f /tmp/ffuf.csv
else
  echo "  ffuf not installed — using curl fallback"
  for p in "${PATHS[@]}"; do
    c=$(curl -s -o /dev/null -w '%{http_code}' "$URL/$p")
    if [[ "$c" == "200" || "$c" == "403" ]]; then
      echo "    HIT  $p  ->  $c"; hits=$((hits+1))
    fi
  done
  if [[ "$hits" == "0" ]]; then
    record "PASS path_enum_basic"
  else
    record "FAIL path_enum_basic"
  fi
fi

# -------- 3. Bearer brute-force productivity --------
hdr "3. Bearer brute-force productivity"
start=$(date +%s)
blocked=0; unauth=0
for i in $(seq 1 100); do
  c=$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer wrong-$i" -X POST "$URL/v1/models")
  case "$c" in
    429) blocked=$((blocked+1));;
    401) unauth=$((unauth+1));;
  esac
done
elapsed=$(( $(date +%s) - start ))
[[ $elapsed -eq 0 ]] && elapsed=1
rate=$(( 100 / elapsed ))
echo "  100 wrong-bearer attempts in ${elapsed}s  (${rate} req/s)"
echo "  401: $unauth   429: $blocked"
if [[ "$blocked" -gt 50 ]]; then
  echo "  rate limiter active — attacker capped quickly"
  record "PASS bearer_ratelimit"
elif [[ "$blocked" -gt 0 ]]; then
  echo "  rate limiter engaged partway through — OK"
  record "PASS bearer_ratelimit_partial"
else
  echo "  no 429 responses — /v1 brute force is unlimited!"
  echo "  expected: after AUTH_FAIL_LIMIT (default 10) wrong tokens, 429 Retry-After"
  record "FAIL bearer_no_ratelimit"
fi

# -------- 4. Basic-auth brute-force productivity --------
hdr "4. Basic-auth brute-force productivity"
if command -v hydra >/dev/null 2>&1; then
  host_port=${URL#*://}
  pw=$(mktemp); printf 'admin\npassword\n123456\nletmein\nadmin123\n' > "$pw"
  out=$(hydra -l admin -P "$pw" -f -q -t 4 "$host_port" http-get / 2>&1 || true)
  rm -f "$pw"
  if grep -q 'host: .*login:' <<<"$out"; then
    echo "  HYDRA CRACKED CREDS — CRITICAL:"
    sed 's/^/    /' <<<"$out"
    record "FAIL hydra_cracked"
  else
    echo "  hydra found no working creds (expected)"
    record "PASS hydra_noop"
  fi
else
  echo "  SKIP: install hydra (\`apt install hydra\` / \`brew install hydra\`) for this check"
  echo "        manual: hydra -l admin -P rockyou.txt ${URL#*://} http-get /"
  record "SKIP hydra_not_installed"
fi

# -------- 5. Nuclei --------
hdr "5. Nuclei exposures + misconfiguration"
if command -v nuclei >/dev/null 2>&1; then
  out=$(nuclei -u "$URL" -silent -severity medium,high,critical \
    -t exposures -t misconfiguration 2>/dev/null || true)
  if [[ -z "$out" ]]; then
    echo "  0 findings (expected)"
    record "PASS nuclei_clean"
  else
    echo "  findings:"
    sed 's/^/    /' <<<"$out"
    record "FAIL nuclei_findings"
  fi
else
  echo "  SKIP: install nuclei from https://github.com/projectdiscovery/nuclei"
  record "SKIP nuclei_not_installed"
fi

# -------- 6. Local secret hygiene --------
hdr "6. Local secret hygiene"
repo_root=$(git -C "$(dirname "$0")" rev-parse --show-toplevel 2>/dev/null || true)
if [[ -n "$repo_root" ]]; then
  cd "$repo_root"
  if [[ -f .env ]]; then
    if git check-ignore -q .env; then
      echo "  .env exists and is gitignored"
      record "PASS env_ignored"
    else
      echo "  .env is NOT gitignored — add '.env' to .gitignore immediately"
      record "FAIL env_not_ignored"
    fi
  else
    echo "  no .env present in repo (OK)"
    record "PASS no_env"
  fi

  leak=$(git log -p -S 'PROXY_TOKEN=' -S 'ADMIN_PASSWORD=' --all 2>/dev/null | head -5 || true)
  if [[ -n "$leak" ]]; then
    echo "  WARN: git history contains strings PROXY_TOKEN= / ADMIN_PASSWORD= — review:"
    echo "        git log -p -S 'PROXY_TOKEN=' --all"
    record "WARN secret_in_history"
  else
    record "PASS history_clean"
  fi

  if command -v gitleaks >/dev/null 2>&1; then
    if gitleaks detect --no-banner --redact -v >/dev/null 2>&1; then
      record "PASS gitleaks"
    else
      echo "  gitleaks reported findings — run \`gitleaks detect -v\` to inspect"
      record "FAIL gitleaks"
    fi
  fi
else
  echo "  not inside a git repo — skipping"
  record "SKIP not_git_repo"
fi

# -------- summary --------
hdr "Summary"
for r in "${RESULTS[@]}"; do
  echo "  $r"
done
echo
echo "  PASS=$PASS  WARN=$WARN  SKIP=$SKIP  FAIL=$FAIL"
if [[ $FAIL -gt 0 ]]; then
  exit 1
fi
exit 0
