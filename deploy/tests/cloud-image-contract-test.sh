#!/usr/bin/env bash
# Static contracts by default. --image TAG adds isolated, offline container smoke.
set -euo pipefail
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)
dockerfile=$repo/deploy/Dockerfile.cloud
compose=$repo/deploy/ec2-cloud-compose.yaml
fail() { echo "cloud image contract failed: $*" >&2; exit 1; }
contains() { grep -Fq -- "$2" "$1" || fail "$1 missing $2"; }
contains "$dockerfile" 'USER 10001:10001'
contains "$dockerfile" 'AS prebuilt'
contains "$dockerfile" './cmd/migrate'
contains "$dockerfile" 'migrate) exec /app/migrate "$@"'
contains "$dockerfile" 'HEALTHCHECK'
contains "$dockerfile" 'all is forbidden'
contains "$compose" 'read_only: true'
contains "$compose" 'cap_drop: [ALL]'
contains "$compose" 'security_opt: [no-new-privileges:true]'
contains "$compose" 'RUNTIME_MULTI_REPLICA_WORKERS: '\''true'\'''
contains "$compose" "command: ['-check']"
contains "$compose" 'create_host_path: false'
! grep -Eq '^[[:space:]]*(postgres|redis|valkey):' "$compose" || fail 'compose provisions a data service'
! grep -Eq '^[[:space:]]*(DATABASE_PASSWORD|REDIS_PASSWORD|JWT_SECRET|TOTP_ENCRYPTION_KEY):' "$compose" || fail 'compose embeds a secret value'
bash -n "$repo/deploy/build_cloud_image.sh" "$0"
if (( $# == 0 )); then
  echo 'cloud image static contracts passed; image build/runtime NOT tested'
  exit 0
fi
[[ $# == 2 && $1 == --image ]] || fail 'Usage: cloud-image-contract-test.sh [--image TAG]'
image=$2
[[ $(docker image inspect --format '{{.Config.User}}' "$image") == 10001:10001 ]] || fail 'image default UID/GID'
common=(--rm --network none --read-only --cap-drop ALL --security-opt no-new-privileges:true
  --pids-limit 128 --memory 192m --cpus 1
  --tmpfs /tmp:rw,noexec,nosuid,nodev,size=16m
  --tmpfs /app/data:rw,noexec,nosuid,nodev,size=32m,uid=10001,gid=10001,mode=0700)
# No DB/Redis/server bootstrap is reachable from help.
help=$(docker run "${common[@]}" -e RUNTIME_ROLE=migrate "$image" -help 2>&1)
[[ $help == *'Usage: migrate'* ]] || fail 'independent migrate -help'
expect_rejected() {
  local output status
  if output=$(docker run "${common[@]}" "$@" "$image" 2>&1); then
    fail 'unsafe role unexpectedly succeeded'
  else
    status=$?
  fi
  [[ $status == 64 && $output == *'cloud image:'* ]] || fail 'expected entrypoint rejection before bootstrap'
}
expect_rejected
expect_rejected -e RUNTIME_ROLE=all
expect_rejected -e RUNTIME_ROLE=unknown
expect_rejected -e RUNTIME_ROLE=gateway -e DATABASE_MIGRATION_MODE=check
expect_rejected -e RUNTIME_ROLE=worker -e RUNTIME_ENABLE_SPLIT_ROLES=true -e DATABASE_MIGRATION_MODE=auto
# Packaging contract only; bypass entrypoint to inspect filesystem with no network.
docker run "${common[@]}" --entrypoint /bin/sh "$image" -ec '
  test "$(id -u)" = 10001
  test -x /app/server && test -x /app/migrate
  test -s /app/LICENSE
  test -s /app/resources/model-pricing/model_prices_and_context_window.json
  touch /app/data/write-test /tmp/write-test
  if touch /app/rootfs-write-must-fail 2>/dev/null; then exit 1; fi
  test ! -e /app/config.yaml
  test ! -e /app/.env
'
echo 'cloud image contracts and network-disabled smoke passed (not DB/role readiness, UI or multiarch validation)'
