#!/usr/bin/env bash
set -euo pipefail

# Apply incident containment without switching application images.
ssh -i "${MULTICA_RELEASE_KEY:-${HOME}/.ssh/mira.pem}" \
  -o BatchMode=yes -o ConnectTimeout=10 \
  "${MULTICA_RELEASE_USER:-deploy}@${MULTICA_RELEASE_HOST:-124.222.33.239}" 'python3 -' <<'PY'
import datetime
import os
import subprocess
import tempfile

config = '/etc/nginx/conf.d/multica.xgrowthai.cn.conf'
marker = '# Multica security ingress guard'

def run(*args):
    return subprocess.run(args, check=True, capture_output=True, text=True).stdout

original = run('sudo', 'cat', config)
if marker in original:
    run('sudo', 'nginx', '-t')
    print('INGRESS_GUARD_ALREADY_CONFIGURED')
    raise SystemExit(0)

anchor = '    location ~ ^/(api|auth|uploads)(/|$) {'
if original.count(anchor) != 1:
    raise RuntimeError('Unexpected Nginx layout; refusing to edit')

proxy = '''        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header X-Forwarded-Proto https;
        client_max_body_size 4k;
'''
guard = '''    # Multica security ingress guard
    location = /uploads { return 404; }
    location ^~ /uploads/design-drafts/ { return 404; }
    location ~ ^/uploads/(.*/)?$ { return 404; }
'''
for route, zone, burst in [('send-code', 'send', 2), ('google', 'send', 2), ('verify-code', 'verify', 5)]:
    guard += f'    location = /auth/{route} {{\n'
    guard += f'        limit_req zone=multica_auth_{zone} burst={burst} nodelay;\n'
    guard += '        limit_req_status 429;\n' + proxy + '    }\n'

updated = '''# Multica authentication limits use the socket peer, never client headers.
limit_req_zone $binary_remote_addr zone=multica_auth_send:1m rate=5r/m;
limit_req_zone $binary_remote_addr zone=multica_auth_verify:1m rate=20r/m;

''' + original.replace(anchor, guard + '\n' + anchor)
stamp = datetime.datetime.now(datetime.timezone.utc).strftime('%Y%m%dT%H%M%SZ')
backup = f'/opt/multica/backups/nginx-security-{stamp}.conf'
run('sudo', 'cp', '-p', config, backup)
run('sudo', 'chmod', '640', backup)
fd, path = tempfile.mkstemp(prefix='multica-nginx-')
try:
    with os.fdopen(fd, 'w') as f:
        f.write(updated)
    run('sudo', 'install', '-o', 'root', '-g', 'root', '-m', '644', path, config)
    try:
        run('sudo', 'nginx', '-t')
        run('sudo', 'systemctl', 'reload', 'nginx')
    except Exception:
        run('sudo', 'cp', '-p', backup, config)
        run('sudo', 'nginx', '-t')
        run('sudo', 'systemctl', 'reload', 'nginx')
        raise RuntimeError('Nginx change failed; original configuration restored') from None
finally:
    os.unlink(path)
print('INGRESS_GUARD_APPLIED backup=' + backup)
PY
