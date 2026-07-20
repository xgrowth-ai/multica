---
name: sync-upstream-multica
description: Sync the xgrowth-ai Multica fork with the upstream multica-ai/multica repository. Use when asked to pull or synchronize upstream/source-repository changes, compare what upstream updated, merge upstream/main into local main, resolve sync conflicts, or report upstream update contents for this Multica repo.
---

# Sync Upstream Multica

Synchronize conservatively. Preserve local fork work, make the merge auditable, and always report what changed upstream.

## Required Context

Read repository rules first:

- `AGENTS.md`
- `CLAUDE.md`

Assume the expected remotes are:

- `origin`: `git@github.com:xgrowth-ai/multica.git`
- `upstream`: `git@github.com:multica-ai/multica.git`

If either remote differs, report it and continue only when the intent is still clear.

## Workflow

1. Inspect the current state.
   - Run `git status --short --branch`.
   - Run `git remote -v`.
   - Identify dirty files before fetching or merging.
   - Do not overwrite, revert, or stage unrelated user changes.

2. Fetch remote references.
   - Run `git fetch --all --tags --prune`.
   - Record the old and new `upstream/main` tips when fetch output shows movement.

3. Compare divergence.
   - Run `git rev-list --left-right --count main...upstream/main`.
   - Run `git rev-list --left-right --count origin/main...upstream/main`.
   - Run `git log --oneline --date=short --pretty=format:'%h %ad %s' <old-upstream>..upstream/main` when an old upstream tip is known.
   - If no old upstream tip is known, use the merge base: `git merge-base main upstream/main`.

4. Summarize upstream changes before merging.
   - Group commits by product area, such as Issues, Autopilots, Daemon/Codex, Desktop, Editor, Self-hosting, Docs, Database.
   - Mention changes that can affect the local fork directly, especially migrations, generated DB code, API schemas, routing, env vars, Docker, or deployment files.

5. Pre-check merge risk.
   - Run `git merge-tree --messages main upstream/main`.
   - If conflicts are predicted, report the conflicted files before starting the real merge.
   - Pay extra attention to local fork feature files and migration numbers.

6. Merge `upstream/main` into local `main`.
   - Use `git merge upstream/main`.
   - Resolve conflicts by preserving both upstream fixes and local fork features when they are compatible.
   - Never drop local fork functionality silently.
   - If a conflict changes product behavior, report the chosen resolution.

7. Validate migration numbering.
   - Check `server/migrations` for duplicate numeric prefixes.
   - If upstream added migrations using the same numbers as local fork migrations, rename local fork migrations to the next unused numbers.
   - Keep concurrent indexes in their own single-statement migration files.
   - Do not add foreign keys or cascading actions while resolving sync conflicts.

8. Verify the result.
   - Always run `pnpm typecheck`.
   - Run focused Go checks at minimum:

```bash
docker run --rm -v "$PWD":/src -w /src/server golang:1.26.1 go test ./internal/handler ./internal/migrations
```

   - If running wider Go tests locally, prefer the CI Go version. If local `go` is unavailable, use Docker.
   - If full `go test ./...` fails because of environment-specific agent/root/home assumptions, report those failures separately from real merge failures.

9. Commit and push when the merge is successful.
   - Commit conflict or follow-up fixes separately when useful for auditability.
   - Push `main` to `origin`.
   - Do not include unrelated dirty files in sync commits.

## Required Final Report

Every sync must end with a concise Chinese report containing:

- synced range: old upstream tip to new upstream tip, or state that there were no upstream changes
- divergence/result: whether local `main` is aligned with `origin/main`, and how many local-only commits remain versus upstream
- update summary: grouped bullets of notable upstream changes
- conflicts/resolutions: files conflicted and how they were resolved, or state that there were no conflicts
- local fork impact: anything affecting custom features, especially design drafts, production deploy skill, env vars, migrations, Docker, or API schemas
- verification: commands run and pass/fail results
- remaining dirty files: list unrelated dirty/untracked files left untouched

If the user only asks to "看看更新了什么" and not to merge, stop after fetch, comparison, and the update report. Do not merge or push unless the user asked to sync/apply the upstream changes.
