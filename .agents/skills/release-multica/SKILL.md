---
name: release-multica
description: Deploy, upgrade, roll back, verify, or troubleshoot the Multica production instance on Tencent Cloud. Use for Multica server initialization, Docker Compose releases, local image build-and-load (no registry), database migration safety, Nginx/TLS configuration, production health checks, and operations for multica.xgrowthai.cn.
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
- Do not assume `ghcr.io/multica-ai/...` images exist on the registry for fork tags. See **Fork release model**.
- Do not assume startup migrations will just run. When the upgrade spans a migration rename or renumber, reconcile `schema_migrations` before bringing the backend up. See **Migration reconciliation**.

## Fork release model

This checkout is the `xgrowth-ai/multica` fork. Two remotes:

- `origin` = `xgrowth-ai/multica` (the fork). The operator has push/admin here. Cut release tags here.
- `upstream` = `multica-ai/multica`. The operator has **no push access**. Upstream's `release.yml` is the only place that ever published to `ghcr.io/multica-ai/...`, and upstream tags lag the fork (fork tags past `v0.4.6` have no upstream counterpart).

Consequences that are easy to get wrong:

- The fork's `release.yml` has never run (Actions history is empty), and even if it ran it would publish to `ghcr.io/xgrowth-ai/...`, **not** the `multica-ai` namespace production pulls. Pushing a tag to `origin` does **not** produce images.
- Therefore `ghcr.io/multica-ai/multica-{backend,web}:<fork-tag>` is generally **not on any registry**. Production gets these images by **building them locally and loading them into the host Docker** (see Release workflow step 4). Treat any image already present on the host (`docker images | grep multica-ai`) as authoritative over the registry.
- Before assuming a tag is a no-op re-deploy, compare the requested tag to the currently-deployed tag. If equal and the goal is just a restart, say so; if the goal is to ship new commits, a newer tag must be cut first.

## Release workflow

1. Confirm the requested version. If absent, inspect repository stable tags and propose the newest stable tag; do not silently choose a prerelease. If shipping new `main` commits that have no tag yet, propose cutting the next patch (`vX.Y.Z+1`) from `origin/main` and confirm with the user before tagging.
2. Run the read-only production check and record current tag, health, free disk, memory, certificate state, and notifier state.
3. Create a timestamped PostgreSQL custom-format dump under `/opt/multica/backups/`. Verify it is non-empty before proceeding.
4. Acquire both exact-tag images. For this fork the default path is **build locally and load** (see [references/production.md](references/production.md) → "Build and load release images"):
   - Check the host first: `ssh deploy@host 'docker images | grep multica-ai | grep <tag>'`. If both images are already loaded, skip building.
   - Otherwise build `Dockerfile` and `Dockerfile.web` on the operator workstation for `--platform linux/amd64`, tagged `ghcr.io/multica-ai/multica-backend:<tag>` and `ghcr.io/multica-ai/multica-web:<tag>`, then stream both into the host with `docker image save --platform=linux/amd64 ... | gzip -1 | ssh ... 'gunzip | docker load'`.
   - Only fall back to `docker pull` from GHCR if the tag is known to exist in the `multica-ai` namespace (it usually does not for fork tags past `v0.4.6`).
5. **Migration pre-flight (do this before changing the deployed tag).** Compare migration filenames between the currently-deployed image and the target image. If any migration was renamed or renumbered, plan the `schema_migrations` reconciliation now — the backend will crash-loop on startup otherwise. See **Migration reconciliation** below.
6. Back up `/opt/multica/.env`, update only `MULTICA_IMAGE_TAG`, and run `docker compose config --quiet`.
7. Run `docker compose up -d`; the backend entrypoint applies migrations automatically. Do not run a second migration command concurrently.
8. Wait for `http://127.0.0.1:8080/healthz` to report both database and migrations `ok`. If the backend enters a restart loop, read `docker compose logs backend` immediately — a migration failure is the most likely cause; apply the reconciliation and `docker compose restart backend`.
9. Verify all of the following:
   - Compose services are running and PostgreSQL is healthy.
   - Frontend loopback returns 200.
   - Public HTTP redirects to HTTPS.
   - Public HTTPS returns 200 with a valid certificate.
   - `/api/config` returns 200.
   - `/ws` reaches the backend rather than returning a proxy 502 (a 4xx from the backend is fine; 502 is not).
   - Backend, frontend, PostgreSQL, and Nginx logs have no new fatal/panic loop.
   - `multica-verification-notifier.service` remains active.
10. Report the deployed tag, health evidence, backup path, image-build method used, and any deliberately unconfigured providers such as SMTP, Redis, or S3.

## Migration reconciliation

`schema_migrations.version` stores the full migration stem (e.g. `197_design_draft`), one row per applied migration. The startup runner iterates migration files in the image and applies any whose stem is not in this table. A migration that was **renamed or renumbered** between releases is therefore treated as unapplied and re-ran, which fails when the object already exists (e.g. `relation "design_draft" already exists`).

When the pre-flight (Release workflow step 5) finds a rename/renumber:

1. Confirm the affected migration's `.up.sql` is byte-identical across the rename (pure rename, like `ec0bc07b1` moving `197_design_draft`→`202_design_draft`). If the SQL also changed, stop and ask the user — do not blindly mark it applied.
2. Confirm the objects the migration creates already exist in the database (table, index).
3. Record the new stems as applied, idempotently, without re-running DDL:

   ```sql
   INSERT INTO schema_migrations (version) VALUES
     ('202_design_draft'),
     ('203_design_draft_workspace_index')
   ON CONFLICT (version) DO NOTHING;
   ```

4. Leave stale rows for the old stems in place (harmless; no image file matches them). Note them in the release report.
5. Then `docker compose restart backend` and re-check `/healthz`.

This is a fork-side hazard driven by upstream sync renumbering local migrations. It is environment-specific bookkeeping, not a code change.

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
3. If the previous image is schema-compatible, confirm the previous-tag images are present on the host (`docker images | grep multica-ai | grep <prev-tag>`); if not, build-and-load them (Release workflow step 4) before rolling back. Then restore the previous `.env` tag backup and run `docker compose up -d`.
4. If it is not compatible, stop and request explicit approval for database restore and its data-loss window.
5. Re-run the complete verification set. Never call a container restart alone a successful rollback.
