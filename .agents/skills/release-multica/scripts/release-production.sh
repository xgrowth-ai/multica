#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel)
CHECK_SCRIPT="$SCRIPT_DIR/check-production.sh"
VERIFY_SCRIPT="$SCRIPT_DIR/verify-production.sh"

HOST="${MULTICA_RELEASE_HOST:-124.222.33.239}"
USER="${MULTICA_RELEASE_USER:-deploy}"
KEY="${MULTICA_RELEASE_KEY:-${HOME}/.ssh/mira.pem}"

usage() {
  cat <<'EOF'
Usage: release-production.sh [all|preflight|prepare|migrations|deploy|verify] [stable-tag]

Stages:
  preflight  Resolve and validate the tag; strictly verify current production.
  prepare    Preflight, publish an existing local tag if needed, build/load exact
             amd64 images, and check migration compatibility. Does not switch services.
  migrations Compare migration manifests in the deployed and prepared images.
  deploy     Require prepared images, back up PostgreSQL and .env, switch the tag,
             wait for health, and strictly verify production.
  verify     Strict read-only verification of the expected production tag.
  all        Run the complete release. This is the default.

If no tag is supplied, HEAD must point at an exact stable tag. This script never
creates a release tag. A release request authorizes publishing an existing tag,
but creating a new tag still requires explicit user confirmation.

After manually reconciling content-identical migration renames, resume with
MULTICA_RELEASE_RECONCILED_TARGET set to the exact target tag. The migration
pre-flight still verifies the rename hashes and both old/new schema_migrations
rows before it permits deployment.
EOF
}

fail() {
  echo "RELEASE_FAILED: $*" >&2
  exit 1
}

run_remote() {
  ssh -i "$KEY" -o BatchMode=yes -o ConnectTimeout=10 "${USER}@${HOST}" "$@"
}

resolve_tag() {
  if [[ -n "${TAG:-}" ]]; then
    return
  fi

  TAG=$(git tag --points-at HEAD --list 'v*' --sort=-version:refname |
    grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -1 || true)
  if [[ -n "$TAG" ]]; then
    echo "resolved_tag=$TAG source=HEAD"
    return
  fi

  latest=$(git tag --list 'v*' --sort=-version:refname |
    grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -1 || true)
  if [[ -n "$latest" ]]; then
    IFS=. read -r major minor patch <<<"${latest#v}"
    next="v${major}.${minor}.$((patch + 1))"
    fail "HEAD has no stable tag; confirm creation of $next before releasing"
  fi
  fail "HEAD has no stable tag and no previous stable tag exists"
}

read_remote_tag_commit() {
  local lines peeled base
  lines=$(git ls-remote --tags origin "refs/tags/$TAG" "refs/tags/$TAG^{}")
  peeled=$(awk '$2 ~ /\^\{\}$/ {print $1}' <<<"$lines")
  base=$(awk '$2 !~ /\^\{\}$/ {print $1}' <<<"$lines")
  REMOTE_TAG_COMMIT="${peeled:-$base}"
}

validate_local_release() {
  cd "$REPO_ROOT"
  [[ "$TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "not a stable tag: $TAG"
  git rev-parse -q --verify "refs/tags/$TAG" >/dev/null || fail "local tag does not exist: $TAG"
  TAG_COMMIT=$(git rev-parse "$TAG^{commit}")
  TAG_COMMIT_SHORT=$(git rev-parse --short "$TAG^{commit}")
  git rev-parse -q --verify refs/heads/main >/dev/null || fail "local main branch does not exist"
  git merge-base --is-ancestor "$TAG_COMMIT" refs/heads/main ||
    fail "$TAG is not reachable from local main"
  [[ -r "$KEY" ]] || fail "SSH key is not readable: $KEY"

  read_remote_tag_commit
  if [[ -n "$REMOTE_TAG_COMMIT" && "$REMOTE_TAG_COMMIT" != "$TAG_COMMIT" ]]; then
    fail "origin/$TAG resolves to a different commit"
  fi

  echo "tag=$TAG commit=$TAG_COMMIT_SHORT remote=$([[ -n "$REMOTE_TAG_COMMIT" ]] && echo published || echo missing)"
}

read_current_tag() {
  CURRENT_TAG=$(run_remote \
    "cd /opt/multica && sudo awk -F= '\$1==\"MULTICA_IMAGE_TAG\" {print \$2}' .env")
  [[ "$CURRENT_TAG" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
    fail "production MULTICA_IMAGE_TAG is invalid or missing"
  echo "current_production_tag=$CURRENT_TAG"
}

validate_builder() {
  local builder_info
  command -v docker >/dev/null || fail "docker is not installed locally"
  docker info >/dev/null || fail "local Docker daemon is unavailable"
  builder_info=$(docker buildx inspect --bootstrap)
  grep -q 'linux/amd64' <<<"$builder_info" ||
    fail "local Docker builder does not support linux/amd64"
  echo "builder=ok platform=linux/amd64"
}

validate_capacity() {
  local capacity
  capacity=$(run_remote "df -Pk / | awk 'NR==2 {print \$4}'")
  [[ "$capacity" =~ ^[0-9]+$ ]] || fail "could not read production free disk"
  (( capacity >= 5 * 1024 * 1024 )) || fail "production has less than 5 GiB free disk"
  echo "production_free_disk_kib=$capacity"
}

strict_current_check() {
  "$VERIFY_SCRIPT" --since 15m "$CURRENT_TAG"
  "$CHECK_SCRIPT"
}

run_preflight() {
  validate_local_release
  validate_builder
  run_remote "true" >/dev/null
  validate_capacity
  read_current_tag
  strict_current_check
  if [[ "$CURRENT_TAG" == "$TAG" ]]; then
    echo "release_state=already_deployed"
  else
    echo "release_state=upgrade from=$CURRENT_TAG to=$TAG"
  fi
  echo "PREFLIGHT_OK tag=$TAG"
}

publish_tag_if_needed() {
  local local_main_commit
  read_remote_tag_commit
  if [[ -n "$REMOTE_TAG_COMMIT" ]]; then
    echo "tag_publish=skipped reason=already_published"
    return
  fi

  git fetch --quiet origin main
  git merge-base --is-ancestor origin/main refs/heads/main ||
    fail "local main is not a fast-forward of origin/main"
  git merge-base --is-ancestor "$TAG_COMMIT" refs/heads/main ||
    fail "$TAG is not reachable from local main"

  if ! git merge-base --is-ancestor "$TAG_COMMIT" origin/main; then
    local_main_commit=$(git rev-parse refs/heads/main)
    [[ "$TAG_COMMIT" == "$local_main_commit" ]] ||
      fail "$TAG is not on origin/main and is not the local main tip; refusing to publish unrelated newer commits"
    git push origin refs/heads/main:refs/heads/main
  fi
  git push origin "refs/tags/$TAG:refs/tags/$TAG"
  read_remote_tag_commit
  [[ "$REMOTE_TAG_COMMIT" == "$TAG_COMMIT" ]] || fail "published tag verification failed"
  echo "tag_publish=ok tag=$TAG"
}

host_has_target_images() {
  run_remote "docker image inspect \
    ghcr.io/multica-ai/multica-backend:${TAG} \
    ghcr.io/multica-ai/multica-web:${TAG} >/dev/null 2>&1"
}

verify_target_images() {
  run_remote "bash -s" -- "$TAG" <<'REMOTE'
set -euo pipefail
tag="$1"
for image in backend web; do
  ref="ghcr.io/multica-ai/multica-${image}:${tag}"
  platform=$(docker image inspect "$ref" --format '{{.Os}}/{{.Architecture}}')
  [[ "$platform" == "linux/amd64" ]] || {
    echo "unexpected image platform for $ref: $platform" >&2
    exit 1
  }
  echo "image=$ref platform=$platform"
done
REMOTE
}

build_and_load_images() (
  if host_has_target_images; then
    echo "image_delivery=skipped reason=both_images_already_loaded"
    verify_target_images
    return
  fi

  local build_dir release_date
  build_dir=$(mktemp -d "${TMPDIR:-/tmp}/multica-release.XXXXXX")
  cleanup_build_dir() {
    rm -rf -- "$build_dir"
  }
  trap cleanup_build_dir EXIT

  git archive "$TAG_COMMIT" | tar -x -C "$build_dir"
  release_date=$(date -u +%Y-%m-%dT%H:%M:%SZ)

  echo "build=backend tag=$TAG source=$TAG_COMMIT_SHORT"
  docker buildx build --progress=plain --platform linux/amd64 --load \
    -f "$build_dir/Dockerfile" \
    --build-arg VERSION="$TAG" \
    --build-arg COMMIT="$TAG_COMMIT_SHORT" \
    --build-arg DATE="$release_date" \
    -t "ghcr.io/multica-ai/multica-backend:${TAG}" "$build_dir"

  echo "build=web tag=$TAG source=$TAG_COMMIT_SHORT"
  docker buildx build --progress=plain --platform linux/amd64 --load \
    -f "$build_dir/Dockerfile.web" \
    --build-arg NEXT_PUBLIC_APP_VERSION="$TAG" \
    -t "ghcr.io/multica-ai/multica-web:${TAG}" "$build_dir"

  local image_platforms unexpected_platforms
  image_platforms=$(docker image inspect \
    "ghcr.io/multica-ai/multica-backend:${TAG}" \
    "ghcr.io/multica-ai/multica-web:${TAG}" \
    --format '{{.RepoTags}} platform={{.Os}}/{{.Architecture}}')
  unexpected_platforms=$(grep -v 'platform=linux/amd64' <<<"$image_platforms" || true)
  [[ -z "$unexpected_platforms" ]] || fail "local target image has an unexpected platform"
  printf '%s\n' "$image_platforms"

  echo "image_delivery=streaming method=local-build-and-load"
  docker image save --platform=linux/amd64 \
    "ghcr.io/multica-ai/multica-backend:${TAG}" \
    "ghcr.io/multica-ai/multica-web:${TAG}" |
    gzip -1 |
    ssh -i "$KEY" -o BatchMode=yes "${USER}@${HOST}" 'gunzip | docker load'

  verify_target_images
  echo "image_delivery=ok method=local-build-and-load"
)

migration_preflight() {
  local current_tag="$1" reconciled_target
  # SSH assembles command arguments into a remote shell command, which drops an
  # empty trailing argument. Keep the third positional parameter present while
  # using a value that can never equal a valid release tag.
  reconciled_target="${MULTICA_RELEASE_RECONCILED_TARGET:-__not_reconciled__}"
  run_remote "bash -s" -- "$current_tag" "$TAG" "$reconciled_target" <<'REMOTE'
set -euo pipefail
current_tag="$1"
target_tag="$2"
reconciled_target="$3"
work_dir=$(mktemp -d /tmp/multica-migrations.XXXXXX)
trap 'rm -rf -- "$work_dir"' EXIT
current_image=$(docker inspect -f '{{.Image}}' multica-backend-1)

manifest() {
  local image_ref="$1"
  docker run --rm --entrypoint sh "$image_ref" -c '
    for file in /app/migrations/*; do
      [ -f "$file" ] || continue
      sha256sum "$file"
    done
  ' | awk '{name=$2; sub(/^.*\//, "", name); print name "|" $1}' | sort
}

manifest "$current_image" > "$work_dir/current"
manifest "ghcr.io/multica-ai/multica-backend:${target_tag}" > "$work_dir/target"
[[ -s "$work_dir/current" && -s "$work_dir/target" ]] || {
  echo "migration manifest is empty" >&2
  exit 1
}

cut -d'|' -f1 "$work_dir/current" > "$work_dir/current-names"
cut -d'|' -f1 "$work_dir/target" > "$work_dir/target-names"
comm -23 "$work_dir/current-names" "$work_dir/target-names" > "$work_dir/missing"
comm -13 "$work_dir/current-names" "$work_dir/target-names" > "$work_dir/added"
join -t '|' -j 1 "$work_dir/current" "$work_dir/target" |
  awk -F'|' '$2 != $3 {print $1 "|" $2 "|" $3}' > "$work_dir/changed"

current_count=$(wc -l < "$work_dir/current" | tr -d ' ')
target_count=$(wc -l < "$work_dir/target" | tr -d ' ')
added_count=$(wc -l < "$work_dir/added" | tr -d ' ')

if [[ -s "$work_dir/missing" || -s "$work_dir/changed" ]]; then
  echo "MIGRATION_PREFLIGHT_BLOCKED current=$current_tag target=$target_tag" >&2
  if [[ -s "$work_dir/missing" ]]; then
    echo "migration_files_missing_from_target:" >&2
    sed 's/^/  /' "$work_dir/missing" >&2
    echo "content_identical_rename_candidates:" >&2
    while IFS= read -r old_name; do
      old_hash=$(awk -F'|' -v name="$old_name" '$1==name {print $2}' "$work_dir/current")
      awk -F'|' -v old="$old_name" -v hash="$old_hash" \
        '$2==hash {print "  " old " -> " $1}' "$work_dir/target" >&2
    done < "$work_dir/missing"
  fi
  if [[ -s "$work_dir/changed" ]]; then
    echo "migration_files_changed_in_place:" >&2
    cut -d'|' -f1 "$work_dir/changed" | sed 's/^/  /' >&2
  fi

  # A reconciliation acknowledgement is deliberately narrow: it applies to
  # one exact target tag, never accepts changed-in-place SQL, requires every
  # missing file to have exactly one byte-identical target candidate, and
  # checks that both the old and new up-migration stems are already recorded.
  # This makes the resume path auditable without creating a generic skip flag.
  if [[ "$reconciled_target" == "$target_tag" && ! -s "$work_dir/changed" ]]; then
    reconciliation_ok=true
    checked_up_stems=0
    while IFS= read -r old_name; do
      old_hash=$(awk -F'|' -v name="$old_name" '$1==name {print $2}' "$work_dir/current")
      mapfile -t candidates < <(awk -F'|' -v hash="$old_hash" '$2==hash {print $1}' "$work_dir/target")
      if [[ ${#candidates[@]} -ne 1 ]]; then
        reconciliation_ok=false
        echo "reconciliation candidate count for $old_name is ${#candidates[@]}, want 1" >&2
        continue
      fi
      if [[ "$old_name" == *.up.sql ]]; then
        old_stem="${old_name%.up.sql}"
        new_stem="${candidates[0]%.up.sql}"
        if [[ ! "$old_stem" =~ ^[0-9]+_[a-z0-9_]+$ || ! "$new_stem" =~ ^[0-9]+_[a-z0-9_]+$ ]]; then
          reconciliation_ok=false
          echo "invalid migration stem in reconciliation: $old_stem -> $new_stem" >&2
          continue
        fi
        for stem in "$old_stem" "$new_stem"; do
          applied=$(docker exec multica-postgres-1 psql -U multica -d multica -X -Atc \
            "SELECT 1 FROM schema_migrations WHERE version = '$stem'")
          if [[ "$applied" != "1" ]]; then
            reconciliation_ok=false
            echo "schema_migrations is missing reconciled stem: $stem" >&2
          fi
        done
        checked_up_stems=$((checked_up_stems + 1))
      fi
    done < "$work_dir/missing"

    if [[ "$reconciliation_ok" == true && "$checked_up_stems" -gt 0 ]]; then
      echo "MIGRATION_PREFLIGHT_RECONCILED current=$current_tag target=$target_tag checked_up_stems=$checked_up_stems"
      exit 0
    fi
    echo "migration reconciliation acknowledgement could not be verified" >&2
  fi
  exit 2
fi

echo "MIGRATION_PREFLIGHT_OK current_count=$current_count target_count=$target_count added=$added_count"
if [[ -s "$work_dir/added" ]]; then
  echo "new_migration_files:"
  sed 's/^/  /' "$work_dir/added"
fi
REMOTE
}

require_prepared_images() {
  host_has_target_images || fail "target images are not loaded; run the prepare stage first"
  verify_target_images
}

deploy_target() {
  local started
  started=$(date -u +%Y-%m-%dT%H:%M:%SZ)

  run_remote "bash -s" -- "$CURRENT_TAG" "$TAG" "$started" <<'REMOTE'
set -euo pipefail
current_tag="$1"
target_tag="$2"
started="$3"
cd /opt/multica

if [[ "$current_tag" != "$target_tag" ]]; then
  sudo install -d -m 750 -o deploy -g deploy /opt/multica/backups
  stamp=$(date -u +%Y%m%dT%H%M%SZ)
  db_backup="/opt/multica/backups/multica-${stamp}.dump"
  env_backup="/opt/multica/backups/env-pre-${target_tag}-${stamp}"

  umask 027
  docker compose exec -T postgres pg_dump -U multica -d multica -Fc > "$db_backup"
  chmod 640 "$db_backup"
  test -s "$db_backup"
  docker compose exec -T postgres pg_restore --list < "$db_backup" >/dev/null
  echo "database_backup=$db_backup bytes=$(stat -c %s "$db_backup") archive=valid"

  tag_count=$(sudo awk -F= '$1=="MULTICA_IMAGE_TAG" {count++} END {print count+0}' .env)
  [[ "$tag_count" -eq 1 ]] || {
    echo "MULTICA_IMAGE_TAG must occur exactly once" >&2
    exit 1
  }
  actual_tag=$(sudo awk -F= '$1=="MULTICA_IMAGE_TAG" {print $2}' .env)
  [[ "$actual_tag" == "$current_tag" ]] || {
    echo "production tag changed during release" >&2
    exit 1
  }

  sudo cp --preserve=mode,ownership,timestamps .env "$env_backup"
  before_without_tag=$(sudo awk -F= '$1!="MULTICA_IMAGE_TAG"' "$env_backup" | sha256sum | cut -d' ' -f1)
  sudo sed -i "s/^MULTICA_IMAGE_TAG=.*/MULTICA_IMAGE_TAG=${target_tag}/" .env
  after_without_tag=$(sudo awk -F= '$1!="MULTICA_IMAGE_TAG"' .env | sha256sum | cut -d' ' -f1)
  [[ "$before_without_tag" == "$after_without_tag" ]] || {
    sudo cp --preserve=mode,ownership,timestamps "$env_backup" .env
    echo "non-tag .env content changed; restored backup" >&2
    exit 1
  }
  [[ $(sudo stat -c '%U:%G %a' .env) == "root:deploy 640" ]] || {
    sudo cp --preserve=mode,ownership,timestamps "$env_backup" .env
    echo ".env permissions changed; restored backup" >&2
    exit 1
  }
  if ! docker compose config --quiet; then
    sudo cp --preserve=mode,ownership,timestamps "$env_backup" .env
    echo "compose config failed; restored .env backup" >&2
    exit 1
  fi
  echo "environment_backup=$env_backup owner=root:deploy mode=640"
else
  echo "version_switch=skipped reason=target_tag_already_configured"
  docker compose config --quiet
fi

echo "release_started=$started"
docker compose up -d

# Compose should recreate services when the image reference changes, but check
# the observable container config instead of trusting that convergence. If an
# older Compose invocation leaves either application container on the previous
# tag, force-recreate only backend/frontend; PostgreSQL stays untouched.
expected_backend="ghcr.io/multica-ai/multica-backend:${target_tag}"
expected_frontend="ghcr.io/multica-ai/multica-web:${target_tag}"
actual_backend=$(docker inspect -f '{{.Config.Image}}' multica-backend-1 2>/dev/null || true)
actual_frontend=$(docker inspect -f '{{.Config.Image}}' multica-frontend-1 2>/dev/null || true)
if [[ "$actual_backend" != "$expected_backend" || "$actual_frontend" != "$expected_frontend" ]]; then
  echo "compose_convergence=retry backend=$actual_backend frontend=$actual_frontend"
  docker compose up -d --no-deps --force-recreate backend frontend
fi

actual_backend=$(docker inspect -f '{{.Config.Image}}' multica-backend-1)
actual_frontend=$(docker inspect -f '{{.Config.Image}}' multica-frontend-1)
[[ "$actual_backend" == "$expected_backend" ]] || {
  echo "backend container image did not converge: $actual_backend" >&2
  exit 1
}
[[ "$actual_frontend" == "$expected_frontend" ]] || {
  echo "frontend container image did not converge: $actual_frontend" >&2
  exit 1
}

for _ in $(seq 1 60); do
  health=$(curl -fsS http://127.0.0.1:8080/healthz 2>/dev/null || true)
  frontend_code=$(curl -sS -o /dev/null -w '%{http_code}' http://127.0.0.1:3000/ 2>/dev/null || true)
  if grep -Eq '"db"[[:space:]]*:[[:space:]]*"ok"' <<<"$health" &&
    grep -Eq '"migrations"[[:space:]]*:[[:space:]]*"ok"' <<<"$health" &&
    [[ "$frontend_code" == "200" ]]; then
      echo "backend_health=$health"
      echo "frontend_health=http_$frontend_code"
      exit 0
  fi
  sleep 2
done

echo "application health did not become ready" >&2
docker compose ps >&2
docker compose logs --tail=200 backend frontend >&2
exit 1
REMOTE

  "$VERIFY_SCRIPT" --since "$started" "$TAG"
}

MODE="all"
TAG=""
if [[ $# -gt 0 ]]; then
  case "$1" in
    all|preflight|prepare|migrations|deploy|verify)
      MODE="$1"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
  esac
fi
[[ $# -le 1 ]] || {
  usage >&2
  exit 2
}
TAG="${1:-}"

cd "$REPO_ROOT"
resolve_tag

case "$MODE" in
  preflight)
    run_preflight
    ;;
  prepare)
    run_preflight
    if [[ "$CURRENT_TAG" == "$TAG" ]]; then
      publish_tag_if_needed
      echo "PREPARE_OK tag=$TAG reason=already_deployed"
      exit 0
    fi
    publish_tag_if_needed
    build_and_load_images
    migration_preflight "$CURRENT_TAG"
    echo "PREPARE_OK tag=$TAG"
    ;;
  migrations)
    validate_local_release
    read_current_tag
    require_prepared_images
    migration_preflight "$CURRENT_TAG"
    ;;
  deploy)
    validate_local_release
    read_current_tag
    if [[ "$CURRENT_TAG" != "$TAG" ]]; then
      strict_current_check
    else
      echo "deploy_resume=target_tag_already_configured"
    fi
    [[ -n "$REMOTE_TAG_COMMIT" ]] || fail "tag is not published; run the prepare stage first"
    require_prepared_images
    migration_preflight "$CURRENT_TAG"
    deploy_target
    echo "RELEASE_OK tag=$TAG"
    ;;
  verify)
    validate_local_release
    "$VERIFY_SCRIPT" "$TAG"
    ;;
  all)
    run_preflight
    if [[ "$CURRENT_TAG" == "$TAG" ]]; then
      publish_tag_if_needed
      echo "RELEASE_OK tag=$TAG reason=already_deployed"
      exit 0
    fi
    publish_tag_if_needed
    build_and_load_images
    migration_preflight "$CURRENT_TAG"
    deploy_target
    echo "RELEASE_OK tag=$TAG"
    ;;
esac
