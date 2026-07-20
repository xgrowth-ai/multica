# Production reference

## Target

| Item | Value |
| --- | --- |
| Domain | `multica.xgrowthai.cn` |
| Public IP | `124.222.33.239` |
| Tencent instance | `ins-n5z1wcml` |
| Region | `ap-shanghai` |
| SSH | `deploy@124.222.33.239` with `~/.ssh/mira.pem` |
| OS | Rocky Linux 9.4 |
| Architecture | `linux/amd64` |
| Capacity | 2 vCPU, 3.6 GiB RAM, 69 GiB root disk, 2 GiB swap |

## Paths and services

| Item | Value |
| --- | --- |
| Compose directory | `/opt/multica` |
| Compose file | `/opt/multica/docker-compose.yml` |
| Runtime env | `/opt/multica/.env` (`root:deploy`, `640`) |
| Backups | `/opt/multica/backups` |
| Nginx config | `/etc/nginx/conf.d/multica.xgrowthai.cn.conf` |
| Certificate | `/etc/letsencrypt/live/multica.xgrowthai.cn/` |
| Notifier files | `/opt/multica/notifier/` |
| Notifier unit | `multica-verification-notifier.service` |

Compose project name is `multica`. Expected containers:

- `multica-postgres-1`: `pgvector/pgvector:pg17`
- `multica-backend-1`: `ghcr.io/multica-ai/multica-backend:<exact-tag>`
- `multica-frontend-1`: `ghcr.io/multica-ai/multica-web:<exact-tag>`

Host bindings:

- Frontend: `127.0.0.1:3000`
- Backend: `127.0.0.1:8080`
- PostgreSQL: Docker network only
- Nginx: public 80/443

## Runtime behavior

- Backend startup runs `./migrate up` before serving traffic.
- Local uploads live in the `multica_backend_uploads` Docker volume unless S3 is configured.
- Database data lives in `multica_pgdata`.
- SMTP/Resend may be unset; in that mode codes appear in backend logs.
- A host-level notifier forwards new development-mode verification-code log lines to Feishu. Its webhook is stored only in `/opt/multica/notifier/notifier.env`.
- Redis may be unset for this single-node deployment; realtime then uses the in-memory hub and auth rate limiting is disabled.
- `/docs` may log connection refusal to port 4000 because the self-host stack does not include the separate docs app. Treat it as a docs-only issue, not main-app failure.

## PostgreSQL backup

Create the directory once with restrictive ownership:

```bash
sudo install -d -m 750 -o deploy -g deploy /opt/multica/backups
```

Create a backup without exposing the database password:

```bash
stamp=$(date -u +%Y%m%dT%H%M%SZ)
cd /opt/multica
docker compose exec -T postgres \
  pg_dump -U multica -d multica -Fc \
  > "/opt/multica/backups/multica-${stamp}.dump"
test -s "/opt/multica/backups/multica-${stamp}.dump"
```

Record the dump path in the release report. Do not claim disaster recovery is established until a restore has been tested separately.

## Trusted GHCR relay fallback

Use this only when direct pulls stall. Run from the trusted operator workstation:

```bash
tag=vX.Y.Z
docker pull --platform linux/amd64 "ghcr.io/multica-ai/multica-backend:${tag}"
docker pull --platform linux/amd64 "ghcr.io/multica-ai/multica-web:${tag}"
docker image save --platform=linux/amd64 \
  "ghcr.io/multica-ai/multica-backend:${tag}" \
  "ghcr.io/multica-ai/multica-web:${tag}" |
  gzip -1 |
  ssh -i ~/.ssh/mira.pem deploy@124.222.33.239 'gunzip | docker load'
```

Do not route release images through an arbitrary public mirror.

## Known boundary

Tencent security groups are outside the host. If HTTP works, server-local HTTPS works, and public HTTPS times out, require inbound TCP 443 in the instance security group. Host firewalld alone cannot fix it.
