#!/usr/bin/env bash
set -euo pipefail

# Prepare the explicit verification channel without changing the image tag.
# The webhook stays on the host and is never returned through command output.
ssh -i "${MULTICA_RELEASE_KEY:-${HOME}/.ssh/mira.pem}" \
  -o BatchMode=yes -o ConnectTimeout=10 \
  "${MULTICA_RELEASE_USER:-deploy}@${MULTICA_RELEASE_HOST:-124.222.33.239}" 'python3 -' <<'PY'
import datetime
import ipaddress
import json
import os
import re
import subprocess
import tempfile
import urllib.parse

def run(*args):
    p = subprocess.run(args, capture_output=True, text=True)
    if p.returncode:
        raise RuntimeError('Configuration operation failed: ' + args[0])
    return p.stdout

def env_values(raw):
    result = {}
    for line in raw.splitlines():
        if '=' in line and not line.lstrip().startswith('#'):
            key, value = line.split('=', 1)
            result[key.strip()] = value.strip().strip('"').strip("'")
    return result

env_path = '/opt/multica/.env'
compose_path = '/opt/multica/docker-compose.yml'
original_env = run('sudo', 'cat', env_path)
original_compose = run('sudo', 'cat', compose_path)
notifier = env_values(run('sudo', 'cat', '/opt/multica/notifier/notifier.env'))
webhook = notifier.get('FEISHU_WEBHOOK_URL', '')
parsed = urllib.parse.urlsplit(webhook)
if parsed.scheme != 'https' or not parsed.hostname or any(c in webhook for c in "\r\n'\"$"):
    raise RuntimeError('Existing notifier webhook is invalid; refusing to prepare delivery')

emails = run('docker', 'exec', 'multica-postgres-1', 'psql', '-X', '-U', 'multica', '-d', 'multica', '-Atqc',
             'SELECT email FROM "user" ORDER BY email').splitlines()
if not emails or any(not re.fullmatch(r'[a-zA-Z0-9_.+\-]+@[a-zA-Z0-9.\-]+', e) for e in emails):
    raise RuntimeError('Existing account list is empty or invalid')
backend = json.loads(run('docker', 'inspect', 'multica-backend-1'))[0]
gateways = [str(ipaddress.ip_address(n['Gateway'])) + '/' + str(ipaddress.ip_address(n['Gateway']).max_prefixlen)
            for n in backend['NetworkSettings']['Networks'].values() if n.get('Gateway')]
settings = {
    'VERIFICATION_CODE_DELIVERY': 'feishu',
    'FEISHU_VERIFICATION_WEBHOOK_URL': webhook,
    'ALLOW_SIGNUP': 'false',
    'ALLOWED_EMAILS': ','.join(emails),
    'RATE_LIMIT_TRUSTED_PROXIES': ','.join(['127.0.0.1/32', '::1/128'] + gateways),
}
updated_env = original_env
for key, value in settings.items():
    replacement = key + "='" + value + "'"
    if re.search(r'^' + re.escape(key) + r'=.*$', updated_env, re.M):
        updated_env = re.sub(r'^' + re.escape(key) + r'=.*$', lambda _: replacement, updated_env, flags=re.M)
    else:
        updated_env = updated_env.rstrip('\n') + '\n' + replacement + '\n'

# Add only non-secret interpolation expressions to the backend environment.
anchor = '      RESEND_API_KEY:'
lines = original_compose.splitlines(keepends=True)
indices = [i for i, line in enumerate(lines) if line.startswith(anchor)]
if len(indices) != 1:
    raise RuntimeError('Unexpected Compose environment layout')
expressions = {
    'VERIFICATION_CODE_DELIVERY': '${VERIFICATION_CODE_DELIVERY:-email}',
    'FEISHU_VERIFICATION_WEBHOOK_URL': '${FEISHU_VERIFICATION_WEBHOOK_URL:-}',
    'RATE_LIMIT_TRUSTED_PROXIES': '${RATE_LIMIT_TRUSTED_PROXIES:-}',
    'RATE_LIMIT_AUTH': '${RATE_LIMIT_AUTH:-5}',
    'RATE_LIMIT_AUTH_VERIFY': '${RATE_LIMIT_AUTH_VERIFY:-20}',
}
insert = ''.join('      ' + key + ': ' + value + '\n' for key, value in expressions.items()
                 if not re.search(r'^      ' + re.escape(key) + ':', original_compose, re.M))
lines.insert(indices[0], insert)
updated_compose = ''.join(lines)
if updated_env == original_env and updated_compose == original_compose:
    print('FEISHU_CONFIGURATION_ALREADY_PREPARED')
    raise SystemExit(0)

stamp = datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ')
env_backup = '/opt/multica/backups/env-security-' + stamp
compose_backup = '/opt/multica/backups/compose-security-' + stamp + '.yml'
for source, backup in [(env_path, env_backup), (compose_path, compose_backup)]:
    run('sudo', 'cp', '-p', source, backup)
    run('sudo', 'chmod', '640', backup)

def install(content, destination, mode):
    fd, path = tempfile.mkstemp(prefix='multica-security-config-')
    try:
        with os.fdopen(fd, 'w') as f:
            f.write(content)
        run('sudo', 'install', '-o', 'root', '-g', 'deploy', '-m', mode, path, destination)
    finally:
        os.unlink(path)

try:
    install(updated_env, env_path, '640')
    install(updated_compose, compose_path, '640')
    # Quiet validation cannot expose interpolated environment values.
    run('docker', 'compose', '--project-directory', '/opt/multica', '-f', compose_path, 'config', '--quiet')
except Exception:
    run('sudo', 'cp', '-p', env_backup, env_path)
    run('sudo', 'cp', '-p', compose_backup, compose_path)
    raise RuntimeError('Configuration validation failed; originals restored') from None
print('FEISHU_CONFIGURATION_PREPARED accounts=' + str(len(emails)))
print('BACKUPS env=' + env_backup + ' compose=' + compose_backup)
print('Services have not been restarted; activate with the verified security release.')
PY
