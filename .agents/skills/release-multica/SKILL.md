---
name: release-multica
description: Deploy, upgrade, roll back, verify, or troubleshoot the Multica production instance on Tencent Cloud. Use for Multica production releases, exact-tag local image build-and-load, database migration safety, Docker Compose operations, Nginx/TLS checks, host bootstrap, rollback, and incidents involving multica.xgrowthai.cn.
---

# Release Multica

Operate production conservatively. Read [references/production.md](references/production.md) before any SSH or deployment action. Treat `CLAUDE.md`, Compose files, Dockerfiles, and repository migrations as authoritative when they differ from this skill.

## Use the deterministic scripts

Do not reconstruct the normal release with ad hoc shell commands. Run the bundled scripts from the repository root:

```bash
# Complete release; omit the tag only when HEAD already has an exact stable tag.
.agents/skills/release-multica/scripts/release-production.sh all [vX.Y.Z]

# Read-only checks.
.agents/skills/release-multica/scripts/release-production.sh preflight [vX.Y.Z]
.agents/skills/release-multica/scripts/release-production.sh verify vX.Y.Z
.agents/skills/release-multica/scripts/check-production.sh
```

Use phases to resume safely:

- `prepare`: publish an existing tag if needed, build/load images, and run migration pre-flight without switching services.
- `migrations`: rerun only the read-only image migration comparison after images are prepared.
- `deploy`: require prepared images, back up PostgreSQL and `.env`, switch the tag, wait for health, and verify.
- `verify`: strictly verify an expected tag without mutation.

The release script is idempotent around observable production state. If production already runs the target tag, `all` performs strict verification and exits without another backup or restart.

## Resolve the version

- If the user supplies a stable tag, use it exactly.
- If no tag is supplied and HEAD has an exact stable tag, use that tag.
- If HEAD has no stable tag, stop and ask before creating the next patch tag. The script proposes the version but never creates it.
- A release request authorizes pushing an existing local `main` commit and existing tag to `origin`; it does not authorize inventing or creating a tag.
- If production already has the target tag, treat it as a verification request unless the user explicitly asks for a restart.
- Never deploy a prerelease or `latest` implicitly.

## Release workflow

1. Run `release-production.sh all [tag]`.
2. Let the script perform strict baseline verification, exact-tag publication, immutable-source amd64 builds, direct image loading, migration comparison, backups, Compose deployment, health waiting, and final verification.
3. If the script reports `MIGRATION_PREFLIGHT_BLOCKED`, stop before changing `.env`. Follow **Migration reconciliation**.
4. If startup health fails, inspect backend logs immediately. Do not run a second migration process; the backend entrypoint already runs migrations.
5. Report the exact tag, health evidence, database and `.env` backup paths, image delivery method, and optional provider state.

## Migration reconciliation

The pre-flight permits new migration files but blocks when a previously shipped filename disappears or its content changes. A missing filename can mean an upstream-sync rename/renumber that would otherwise re-run DDL.

When blocked:

1. Confirm each suggested rename has byte-identical `.up.sql` content. If SQL changed, stop and ask the user.
2. Confirm the objects created by the old migration already exist in PostgreSQL.
3. Insert only the new full stems into `schema_migrations`, idempotently:

   ```sql
   INSERT INTO schema_migrations (version) VALUES
     ('207_design_draft'),
     ('208_design_draft_workspace_index')
   ON CONFLICT (version) DO NOTHING;
   ```

4. Leave old stems in place and record the reconciliation in the release report.
5. Resume the `deploy` phase with the exact target acknowledgement; never
   re-run the migration DDL manually:

   ```bash
   MULTICA_RELEASE_RECONCILED_TARGET=vX.Y.Z \
     .agents/skills/release-multica/scripts/release-production.sh deploy vX.Y.Z
   ```

   The script still requires byte-identical rename candidates and verifies
   both the old and new stems in production before it permits deployment.

## Diagnose production

Run `scripts/check-production.sh` first. It is observational and deliberately continues across public-endpoint failures so all layers remain visible. Use `scripts/verify-production.sh <tag>` when a pass/fail result is required; it exits non-zero on any failed invariant.

Inspect only the failing layer unless the user asks for a fix. Do not restart or mutate healthy services during diagnosis.

## Roll back

1. Identify the exact previous tag and pre-release database dump.
2. State that image rollback does not reverse forward-applied migrations.
3. Confirm schema compatibility and previous-tag image availability. Use the `prepare` phase for the previous tag if images are absent.
4. If schema-compatible, restore the corresponding `.env` backup, run `docker compose up -d`, and run strict verification for the previous tag.
5. If a database restore is required, stop for explicit approval and state the data-loss window.

## Bootstrap a host

Use the host details and bootstrap checklist in [references/production.md](references/production.md). Audit first; create and independently verify the `deploy` account before disabling root/password SSH. After bootstrap, use the normal release script.

## Hard rules

- Connect as `deploy` with `~/.ssh/mira.pem`; use root SSH only with explicit first-boot recovery authorization.
- Never print private keys, `.env` contents, secrets, passwords, webhook URLs, or verification codes.
- Keep `.env` as `root:deploy` mode `640`.
- Back up PostgreSQL before changing the application tag.
- Keep ports 3000, 8080, and 5432 private; expose only 80/443 through Nginx.
- Preserve `/opt/multica/notifier/` and `multica-verification-notifier.service` outside Compose.
- Never use broad Docker cleanup commands or untrusted registry mirrors.
- Do not assume fork tags exist in GHCR. Fork releases are built locally for `linux/amd64` and loaded directly onto the host.
- Never claim success before `verify-production.sh` passes for the expected tag.
