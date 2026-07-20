---
name: release-multica
description: Deploy, upgrade, roll back, verify, or troubleshoot the Multica production instance on Tencent Cloud. Use for Multica server initialization, Docker Compose releases, GHCR image delivery, database migration safety, Nginx/TLS configuration, production health checks, and operations for multica.xgrowthai.cn.
---

# Release Multica

Operate the production instance conservatively. Lead with the observed state and never claim a release succeeded before end-to-end verification.

## Load production context

Read [references/production.md](references/production.md) before any SSH or deployment action. Treat repository files (`CLAUDE.md`, `docker-compose.selfhost.yml`, `Dockerfile*`, release workflow) as the source of truth when they differ from this skill.

## Choose the workflow

- For status, logs, or diagnosis: run `scripts/check-production.sh`, then inspect only the failing layer. Do not mutate services unless asked to fix them.
- For an upgrade or release: follow **Release workflow**.
- For a new or rebuilt host: follow **Bootstrap workflow**, then **Release workflow**.
- For rollback: follow **Rollback workflow**. State the migration compatibility risk before changing the image tag.

## Hard rules

- Connect as `deploy` with `~/.ssh/mira.pem`. Use root SSH only when the user explicitly authorizes first-boot recovery; create and verify `deploy` before disabling root SSH.
- Never read or print private-key contents, `.env` values, JWT secrets, database passwords, webhook URLs, or verification codes unless the user explicitly requests the code needed for their own login.
- Deploy an exact stable tag such as `v0.4.2`; never deploy `latest` to production.
- Keep ports 3000, 8080, and 5432 private. Publish only 80/443 through Nginx.
- Back up PostgreSQL before changing the deployed application version.
- Never use broad Docker cleanup commands (`docker system prune`, `docker volume prune`, or unscoped image prune).
- Preserve `/opt/multica/notifier/` and `multica-verification-notifier.service`; they are host-managed and outside Compose.
- Do not use an untrusted registry mirror. The configured Tencent mirror may accelerate Docker Hub. Use the trusted local relay fallback for GHCR.
- Keep the production `.env` root-owned and group-readable only (`root:deploy`, mode `640`).

## Release workflow

1. Confirm the requested version. If absent, inspect repository stable tags and propose the newest stable tag; do not silently choose a prerelease.
2. Run the read-only production check and record current tag, health, free disk, memory, certificate state, and notifier state.
3. Create a timestamped PostgreSQL custom-format dump under `/opt/multica/backups/`. Verify it is non-empty before proceeding.
4. Acquire both exact-tag images:
   - First try remote `docker pull` from GHCR.
   - If GHCR blob transfer stalls or times out, stop only the stuck pull. Pull `linux/amd64` locally, then stream `docker image save --platform=linux/amd64 ... | gzip` over SSH into remote `docker load`.
5. Back up `/opt/multica/.env`, update only `MULTICA_IMAGE_TAG`, and run `docker compose config --quiet`.
6. Run `docker compose up -d`; the backend entrypoint applies migrations automatically. Do not run a second migration command concurrently.
7. Wait for `http://127.0.0.1:8080/healthz` to report both database and migrations `ok`.
8. Verify all of the following:
   - Compose services are running and PostgreSQL is healthy.
   - Frontend loopback returns 200.
   - Public HTTP redirects to HTTPS.
   - Public HTTPS returns 200 with a valid certificate.
   - `/api/config` returns 200.
   - `/ws` reaches the backend rather than returning a proxy 502.
   - Backend, frontend, PostgreSQL, and Nginx logs have no new fatal/panic loop.
   - `multica-verification-notifier.service` remains active.
9. Report the deployed tag, health evidence, backup path, and any deliberately unconfigured providers such as SMTP, Redis, or S3.

## Bootstrap workflow

1. Audit OS, CPU, memory, disk, listeners, DNS, Docker, Nginx, firewall, and existing users without mutation.
2. Require at least 2 vCPU, about 4 GiB RAM, and 30 GiB disk for the small production shape. Add 2 GiB swap on a 4 GiB host.
3. Create `deploy`, install its public key, grant controlled passwordless sudo, and verify a separate `deploy` SSH session.
4. Install Docker Engine + Compose, Nginx, firewalld, fail2ban, Certbot, and bounded Docker log rotation.
5. Allow only SSH, HTTP, and HTTPS at the host firewall. Remove unused public services such as Cockpit.
6. Disable root/password SSH only after the independent `deploy` session succeeds.
7. Create `/opt/multica`, generate secrets on the server, install Compose and `.env` with restrictive ownership, then continue with the release workflow.
8. Obtain the TLS certificate only after DNS points to the host and port 80 is reachable. Enable and dry-run certificate renewal.
9. If server-local TLS works but public 443 times out, diagnose the Tencent security group; do not weaken Nginx or remove HTTPS redirect as a workaround.

## Rollback workflow

1. Identify the exact previous tag and the pre-release database dump.
2. Explain that startup migrations are forward-applied and image rollback does not automatically reverse schema migrations.
3. If the previous image is schema-compatible, restore the previous `.env` tag backup and run `docker compose up -d`.
4. If it is not compatible, stop and request explicit approval for database restore and its data-loss window.
5. Re-run the complete verification set. Never call a container restart alone a successful rollback.
