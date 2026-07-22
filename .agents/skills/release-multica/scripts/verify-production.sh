#!/usr/bin/env bash
set -euo pipefail

HOST="${MULTICA_RELEASE_HOST:-124.222.33.239}"
USER="${MULTICA_RELEASE_USER:-deploy}"
KEY="${MULTICA_RELEASE_KEY:-${HOME}/.ssh/mira.pem}"
DOMAIN="${MULTICA_RELEASE_DOMAIN:-multica.xgrowthai.cn}"
EXPECTED_IP="${MULTICA_RELEASE_EXPECTED_IP:-$HOST}"
SINCE="15m"

usage() {
  cat <<'EOF'
Usage: verify-production.sh [--since <RFC3339|Nm>] <stable-tag>

Strictly verifies the Multica production deployment. Any failed invariant
returns a non-zero exit status. No production state is changed.
EOF
}

fail() {
  echo "VERIFY_FAILED: $*" >&2
  exit 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --since)
      [[ $# -ge 2 ]] || fail "--since requires a value"
      SINCE="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    --*)
      fail "unknown option: $1"
      ;;
    *)
      [[ -z "${TAG:-}" ]] || fail "only one tag may be supplied"
      TAG="$1"
      shift
      ;;
  esac
done

[[ -n "${TAG:-}" ]] || {
  usage >&2
  exit 2
}
[[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "not a stable tag: $TAG"
[[ -r "$KEY" ]] || fail "SSH key is not readable: $KEY"

echo "VERIFY target=$TAG domain=$DOMAIN"

dns_addresses=$(dig +short A "$DOMAIN")
grep -Fxq "$EXPECTED_IP" <<<"$dns_addresses" ||
  fail "DNS does not include expected address $EXPECTED_IP (got: ${dns_addresses:-none})"
echo "dns=ok address=$EXPECTED_IP"

http_result=$(curl --noproxy '*' -sS -o /dev/null \
  -w '%{http_code} %{redirect_url}' --connect-timeout 8 --max-time 12 \
  "http://${DOMAIN}/") || fail "public HTTP request failed"
read -r http_code http_redirect <<<"$http_result"
[[ "$http_code" == "301" || "$http_code" == "308" ]] ||
  fail "public HTTP did not redirect (status=$http_code)"
[[ "$http_redirect" == "https://${DOMAIN}/" ]] ||
  fail "unexpected HTTP redirect target: $http_redirect"
echo "public_http=ok status=$http_code"

https_code=$(curl --noproxy '*' -sS -o /dev/null -w '%{http_code}' \
  --connect-timeout 8 --max-time 12 "https://${DOMAIN}/") ||
  fail "public HTTPS request or certificate validation failed"
[[ "$https_code" == "200" ]] || fail "public HTTPS status is $https_code"
echo "public_https=ok status=$https_code"

api_code=$(curl --noproxy '*' -sS -o /dev/null -w '%{http_code}' \
  --connect-timeout 8 --max-time 12 "https://${DOMAIN}/api/config") ||
  fail "/api/config request failed"
[[ "$api_code" == "200" ]] || fail "/api/config status is $api_code"
echo "api_config=ok status=$api_code"

ws_body=$(mktemp "${TMPDIR:-/tmp}/multica-ws.XXXXXX")
trap 'rm -f -- "$ws_body"' EXIT
ws_code=$(curl --noproxy '*' --http1.1 -sS -o "$ws_body" -w '%{http_code}' \
  --connect-timeout 8 --max-time 12 \
  -H 'Connection: Upgrade' \
  -H 'Upgrade: websocket' \
  -H 'Sec-WebSocket-Version: 13' \
  -H 'Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==' \
  "https://${DOMAIN}/ws" || true)
case "$ws_code" in
  400|401|403|426) ;;
  502|000) fail "WebSocket proxy is unavailable (status=$ws_code)" ;;
  *) fail "unexpected unauthenticated WebSocket status: $ws_code" ;;
esac
echo "websocket_proxy=ok status=$ws_code"

ssh -i "$KEY" -o BatchMode=yes -o ConnectTimeout=10 \
  "${USER}@${HOST}" 'bash -s' -- "$TAG" "$SINCE" "$DOMAIN" <<'REMOTE'
set -euo pipefail

tag="$1"
since_arg="$2"
domain="$3"
cd /opt/multica

fail() {
  echo "VERIFY_FAILED: $*" >&2
  exit 1
}

if [[ "$since_arg" =~ ^[0-9]+m$ ]]; then
  minutes="${since_arg%m}"
  since_rfc3339=$(date -u -d "$minutes minutes ago" +%Y-%m-%dT%H:%M:%SZ)
else
  since_rfc3339="$since_arg"
fi

compose_lines=$(docker compose ps --format '{{.Service}}|{{.Image}}|{{.State}}|{{.Health}}')
for service in backend frontend postgres; do
  line=$(grep -E "^${service}\\|" <<<"$compose_lines" || true)
  [[ -n "$line" ]] || fail "$service is missing from docker compose ps"
  state=$(cut -d'|' -f3 <<<"$line")
  [[ "$state" == "running" ]] || fail "$service state is $state"
done

backend_image=$(awk -F'|' '$1=="backend" {print $2}' <<<"$compose_lines")
frontend_image=$(awk -F'|' '$1=="frontend" {print $2}' <<<"$compose_lines")
[[ "$backend_image" == "ghcr.io/multica-ai/multica-backend:${tag}" ]] ||
  fail "backend image is $backend_image"
[[ "$frontend_image" == "ghcr.io/multica-ai/multica-web:${tag}" ]] ||
  fail "frontend image is $frontend_image"

backend_container_image_id=$(docker inspect -f '{{.Image}}' multica-backend-1)
frontend_container_image_id=$(docker inspect -f '{{.Image}}' multica-frontend-1)
backend_tag_image_id=$(docker image inspect "ghcr.io/multica-ai/multica-backend:${tag}" -f '{{.Id}}')
frontend_tag_image_id=$(docker image inspect "ghcr.io/multica-ai/multica-web:${tag}" -f '{{.Id}}')
[[ "$backend_container_image_id" == "$backend_tag_image_id" ]] ||
  fail "backend container does not run the image currently named by $tag"
[[ "$frontend_container_image_id" == "$frontend_tag_image_id" ]] ||
  fail "frontend container does not run the image currently named by $tag"

postgres_health=$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' multica-postgres-1)
[[ "$postgres_health" == "healthy" ]] || fail "PostgreSQL health is $postgres_health"

health=$(curl -fsS http://127.0.0.1:8080/healthz) || fail "backend health endpoint failed"
grep -Eq '"status"[[:space:]]*:[[:space:]]*"ok"' <<<"$health" || fail "backend status is not ok"
grep -Eq '"db"[[:space:]]*:[[:space:]]*"ok"' <<<"$health" || fail "database check is not ok"
grep -Eq '"migrations"[[:space:]]*:[[:space:]]*"ok"' <<<"$health" || fail "migration check is not ok"

frontend_code=$(curl -fsS -o /dev/null -w '%{http_code}' http://127.0.0.1:3000/) ||
  fail "frontend loopback request failed"
[[ "$frontend_code" == "200" ]] || fail "frontend loopback status is $frontend_code"

backend_port=$(docker inspect -f '{{range (index .NetworkSettings.Ports "8080/tcp")}}{{.HostIp}}:{{.HostPort}}{{end}}' multica-backend-1)
frontend_port=$(docker inspect -f '{{range (index .NetworkSettings.Ports "3000/tcp")}}{{.HostIp}}:{{.HostPort}}{{end}}' multica-frontend-1)
postgres_port=$(docker inspect -f '{{range (index .NetworkSettings.Ports "5432/tcp")}}{{.HostIp}}:{{.HostPort}}{{end}}' multica-postgres-1)
[[ "$backend_port" == 127.0.0.1:* ]] || fail "backend is not loopback-bound: $backend_port"
[[ "$frontend_port" == 127.0.0.1:* ]] || fail "frontend is not loopback-bound: $frontend_port"
[[ -z "$postgres_port" ]] || fail "PostgreSQL is published on the host: $postgres_port"

env_metadata=$(sudo stat -c '%U:%G %a' /opt/multica/.env)
[[ "$env_metadata" == "root:deploy 640" ]] || fail "unexpected .env metadata: $env_metadata"

echo | openssl s_client -servername "$domain" -connect 127.0.0.1:443 2>/dev/null |
  openssl x509 -checkend 604800 -noout >/dev/null || fail "certificate expires within seven days"

sudo systemctl is-active --quiet multica-verification-notifier.service ||
  fail "verification notifier is not active"

compose_fatals=$(docker compose logs --since="$since_rfc3339" backend frontend postgres 2>&1 |
  grep -Ei 'fatal|panic|segmentation fault|out of memory|migration.*failed|error starting' || true)
[[ -z "$compose_fatals" ]] || {
  printf '%s\n' "$compose_fatals" >&2
  fail "fatal service log entries found"
}

nginx_fatals=$(sudo journalctl -u nginx --since="$since_rfc3339" --no-pager 2>&1 |
  grep -Ei 'fatal|panic|segmentation fault|out of memory|emerg|alert|crit' || true)
[[ -z "$nginx_fatals" ]] || {
  printf '%s\n' "$nginx_fatals" >&2
  fail "critical Nginx log entries found"
}

echo "compose=ok backend=$backend_image frontend=$frontend_image postgres=healthy"
echo "loopback=ok backend_health=db+migrations frontend=$frontend_code"
echo "bindings=ok backend=$backend_port frontend=$frontend_port postgres=private"
echo "env_permissions=ok owner=root:deploy mode=640"
echo "certificate=ok minimum_remaining=7d"
echo "logs=ok since=$since_rfc3339"
echo "notifier=active"

docker compose exec -T backend sh -c '
for key in SMTP_HOST RESEND_API_KEY REDIS_URL S3_BUCKET; do
  eval "value=\${$key-}"
  if [ -n "$value" ]; then
    echo "provider_${key}=configured"
  else
    echo "provider_${key}=unconfigured"
  fi
done
'
REMOTE

echo "VERIFY_OK tag=$TAG"
