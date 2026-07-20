#!/usr/bin/env bash
set -euo pipefail

HOST="${MULTICA_RELEASE_HOST:-124.222.33.239}"
USER="${MULTICA_RELEASE_USER:-deploy}"
KEY="${MULTICA_RELEASE_KEY:-${HOME}/.ssh/mira.pem}"
DOMAIN="${MULTICA_RELEASE_DOMAIN:-multica.xgrowthai.cn}"

if [[ ! -r "$KEY" ]]; then
  echo "SSH key is not readable: $KEY" >&2
  exit 1
fi

echo "DNS"
dig +short A "$DOMAIN"

echo "PUBLIC_HTTP"
curl --noproxy '*' -fsSI --connect-timeout 8 --max-time 12 \
  "http://${DOMAIN}/" | sed -n '1,8p' || true

echo "PUBLIC_HTTPS"
curl --noproxy '*' -fsSI --connect-timeout 8 --max-time 12 \
  "https://${DOMAIN}/" | sed -n '1,12p' || true

ssh -i "$KEY" -o BatchMode=yes -o ConnectTimeout=10 "${USER}@${HOST}" 'bash -s' <<'REMOTE'
set -euo pipefail
cd /opt/multica

echo "COMPOSE"
docker compose ps

echo "BACKEND_HEALTH"
curl -fsS http://127.0.0.1:8080/healthz
echo

echo "FRONTEND"
curl -fsS -o /dev/null -w 'http=%{http_code}\n' http://127.0.0.1:3000/

echo "RESOURCES"
free -h
df -h /

echo "FIREWALL"
sudo firewall-cmd --list-services

echo "CERTIFICATE"
echo | openssl s_client -servername multica.xgrowthai.cn -connect 127.0.0.1:443 2>/dev/null |
  openssl x509 -noout -subject -issuer -dates

echo "NOTIFIER"
sudo systemctl is-active multica-verification-notifier.service

echo "RECENT_FATALS"
docker compose logs --since=15m --tail=300 backend frontend postgres 2>&1 |
  grep -Ei 'fatal|panic|segmentation fault|out of memory|migration.*failed' || true
REMOTE
