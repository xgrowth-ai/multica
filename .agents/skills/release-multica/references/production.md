# Production reference

## Contents

- [Target](#target)
- [Paths and services](#paths-and-services)
- [Runtime behavior](#runtime-behavior)
- [Automated release](#automated-release)
- [Build and load fallback](#build-and-load-fallback)
- [Migration reconciliation](#migration-reconciliation)
- [PostgreSQL backup](#postgresql-backup)
- [Bootstrap checklist](#bootstrap-checklist)
- [Trusted GHCR relay fallback](#trusted-ghcr-relay-fallback)
- [Known boundary](#known-boundary)

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

## Automated release

Run the deterministic scripts from the repository root:

```bash
# Complete release (default mode is also `all`).
.agents/skills/release-multica/scripts/release-production.sh all [vX.Y.Z]

# Resume or inspect individual phases.
.agents/skills/release-multica/scripts/release-production.sh preflight [vX.Y.Z]
.agents/skills/release-multica/scripts/release-production.sh prepare [vX.Y.Z]
.agents/skills/release-multica/scripts/release-production.sh migrations vX.Y.Z
.agents/skills/release-multica/scripts/release-production.sh deploy [vX.Y.Z]
.agents/skills/release-multica/scripts/release-production.sh verify vX.Y.Z
```

Important behavior:

- An omitted tag resolves only when HEAD already has an exact `vX.Y.Z` tag. The script never creates tags.
- `prepare` may push an existing local `main` and tag to `origin`. It never pushes to `upstream`.
- Builds come from `git archive <tag-commit>`, not the working tree, and use `--progress=plain` without a TTY.
- Host images are authoritative. When both exact-tag images already exist, `prepare` skips the build and transfer.
- Migration pre-flight permits new files, but blocks a missing previously shipped filename or an in-place content change.
- After a verified content-identical rename is reconciled in production, resume with `MULTICA_RELEASE_RECONCILED_TARGET=<exact-target-tag> ... deploy <exact-target-tag>`. The script verifies the rename hashes and both old/new migration stems; this is not a general skip switch.
- `deploy` validates the PostgreSQL archive with `pg_restore --list`, backs up `.env`, proves that only `MULTICA_IMAGE_TAG` changed, preserves `root:deploy 640`, verifies both application containers converged to the exact tag, and waits for backend plus frontend health.
- `verify` is strict. It checks exact images, service health, loopback bindings, public HTTP/HTTPS, `/api/config`, `/ws`, TLS lifetime, logs, notifier state, and optional provider configuration.
- Re-running `all` for the already-deployed tag performs verification without another backup or restart.

The observational `scripts/check-production.sh` intentionally continues after public HTTP failures to show all layers. Do not use its exit status as release success; use the strict verifier.

## Build and load fallback

This fork does not publish release images to the production `multica-ai` GHCR namespace. Production images `ghcr.io/multica-ai/multica-{backend,web}:<tag>` are built on the operator workstation and streamed into the host Docker. The host is `linux/amd64`; the operator workstation is typically Apple Silicon, so build for the target platform explicitly.

Prefer the `prepare` phase. If the script itself needs repair, preserve immutable tag input with a temporary archive:

```bash
tag=vX.Y.Z
tag_commit=$(git rev-parse "${tag}^{commit}")
commit=$(git rev-parse --short "${tag}^{commit}")
date=$(date -u +%Y-%m-%dT%H:%M:%SZ)
build_dir=$(mktemp -d "${TMPDIR:-/tmp}/multica-release.XXXXXX")
git archive "$tag_commit" | tar -x -C "$build_dir"

docker buildx build --progress=plain --platform linux/amd64 --load \
  -f "$build_dir/Dockerfile" \
  --build-arg VERSION="$tag" --build-arg COMMIT="$commit" --build-arg DATE="$date" \
  -t "ghcr.io/multica-ai/multica-backend:${tag}" "$build_dir"

docker buildx build --progress=plain --platform linux/amd64 --load \
  -f "$build_dir/Dockerfile.web" \
  --build-arg NEXT_PUBLIC_APP_VERSION="$tag" \
  -t "ghcr.io/multica-ai/multica-web:${tag}" "$build_dir"

docker image save --platform=linux/amd64 \
  "ghcr.io/multica-ai/multica-backend:${tag}" \
  "ghcr.io/multica-ai/multica-web:${tag}" |
  gzip -1 |
  ssh -i ~/.ssh/mira.pem deploy@124.222.33.239 'gunzip | docker load'
```

Remove only the exact temporary directory after the transfer. Never build a release from an uncommitted working tree.

Verify on the host before flipping the tag:

```bash
ssh -i ~/.ssh/mira.pem deploy@124.222.33.239 'docker images | grep "multica-ai.*<tag>"'
```

If the host already has both images for the tag (re-deploy, retry), skip building. The backend build is fast (Go); the web build is the long pole (Next.js under amd64 emulation, ~10–20 min on Apple Silicon). Building on the host itself is not recommended — the box is too small (2 vCPU, 3.6 GiB) for the Next.js build.

## Migration reconciliation

`schema_migrations(version text, applied_at timestamptz)` holds one row per applied migration, keyed by the full filename stem. A migration renamed/renumbered between releases will re-run on upgrade and fail when its objects already exist. Check first whether the rename was content-identical, confirm the objects exist, then insert the new stems as applied — see the skill's **Migration reconciliation** section.

Design-draft migration history:

- The v0.4.10 release reconciled `197_design_draft`→`202_design_draft` and `198_design_draft_workspace_index`→`203_design_draft_workspace_index`.
- The next release after upstream added migrations `202–206` must reconcile the byte-identical fork migrations again: `202_design_draft`→`207_design_draft` and `203_design_draft_workspace_index`→`208_design_draft_workspace_index`. Production already contains the objects and the old `202/203` stems; insert the new `207/208` stems before backend startup and leave the old rows in place.

The automated pre-flight hashes every migration file inside the current and target backend images. It blocks when:

- a filename present in the current image is absent from the target image; or
- an existing filename has different content.

It prints byte-identical rename candidates but does not write `schema_migrations`. Reconciliation remains an explicit operator decision.

## PostgreSQL backup

Create the directory once with restrictive ownership:

```bash
sudo install -d -m 750 -o deploy -g deploy /opt/multica/backups
```

Create a backup without exposing the database password:

```bash
stamp=$(date -u +%Y%m%dT%H%M%SZ)
cd /opt/multica
umask 027
docker compose exec -T postgres \
  pg_dump -U multica -d multica -Fc \
  > "/opt/multica/backups/multica-${stamp}.dump"
test -s "/opt/multica/backups/multica-${stamp}.dump"
chmod 640 "/opt/multica/backups/multica-${stamp}.dump"
docker compose exec -T postgres pg_restore --list \
  < "/opt/multica/backups/multica-${stamp}.dump" >/dev/null
```

Record the dump path in the release report. Do not claim disaster recovery is established until a restore has been tested separately.

## Bootstrap checklist

1. Audit OS, CPU, memory, disk, listeners, DNS, Docker, Nginx, firewall, and existing users without mutation.
2. Require at least 2 vCPU, about 4 GiB RAM, 30 GiB disk, and 2 GiB swap for this production shape.
3. Create `deploy`, install its public key, grant controlled passwordless sudo, and verify a separate `deploy` SSH session.
4. Install Docker Engine with Compose, Nginx, firewalld, fail2ban, Certbot, and bounded Docker log rotation.
5. Allow only SSH, HTTP, and HTTPS at the host firewall. Remove unused public services such as Cockpit.
6. Disable root/password SSH only after the independent `deploy` session succeeds.
7. Create `/opt/multica`, generate secrets on the host, and install Compose plus `.env` with `root:deploy 640`.
8. Obtain TLS only after DNS and port 80 work; enable and dry-run renewal.
9. Install and verify the host-managed notifier separately, then use the normal automated release.

## Trusted GHCR relay fallback

Secondary path. Use only when the target tag is actually published to the `multica-ai` GHCR namespace — fork tags past `v0.4.6` usually are not, so prefer **Build and load fallback** above. When a remote `docker pull` stalls, run from the trusted operator workstation:

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
