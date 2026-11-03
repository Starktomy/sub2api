#!/usr/bin/env bash
# Local build only: no login, push, cloud calls or repository-wide Docker context.
set -euo pipefail
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
usage() {
  echo 'Usage: build_cloud_image.sh IMAGE [linux/amd64|linux/arm64] [PREBUILT_DIRECTORY]'
  echo 'PREBUILT_DIRECTORY must contain static CGO_ENABLED=0 Linux server and migrate binaries.'
}
if [[ ${1:-} == --help ]]; then usage; exit 0; fi
if (( $# < 1 || $# > 3 )); then usage >&2; exit 64; fi
image=$1
platform=${2:-linux/amd64}
case "$platform" in linux/amd64|linux/arm64) ;; *) usage >&2; exit 64 ;; esac
context=$(mktemp -d "${TMPDIR:-/tmp}/sub2api-cloud-context.XXXXXXXX")
trap 'rm -rf -- "$context"' EXIT
copy_file() {
  local relative=$1
  # Do not follow symlinks into operator directories.
  [[ -f "$repo/$relative" && ! -L "$repo/$relative" ]] || {
    echo "Refusing missing or symlink source: $relative" >&2; exit 1;
  }
  mkdir -p -- "$context/$(dirname -- "$relative")"
  cp -- "$repo/$relative" "$context/$relative"
}
copy_file LICENSE
copy_file deploy/Dockerfile.cloud
# Runtime resources are explicitly allowlisted, not the whole resource tree.
copy_file backend/resources/model-pricing/model_prices_and_context_window.json
if (( $# == 3 )); then
  target=prebuilt
  mkdir -p "$context/prebuilt"
  for binary in server migrate; do
    source_file=$3/$binary
    [[ -f "$source_file" && ! -L "$source_file" ]] || {
      echo "Missing regular prebuilt binary: $binary" >&2; exit 1;
    }
    description=$(file -b -- "$source_file")
    case "$platform:$description" in
      linux/amd64:*ELF*64-bit*x86-64*statically\ linked*|linux/arm64:*ELF*64-bit*ARM\ aarch64*statically\ linked*) ;;
      *) echo "Prebuilt $binary must be a static Linux binary for $platform" >&2; exit 1 ;;
    esac
    cp -- "$source_file" "$context/prebuilt/$binary"
  done
else
  target=cloud
  # Only tracked production Go source and known embedded assets; no .git,
  # accounts, env files, private config, plugins, caches or host credentials.
  while IFS= read -r -d '' relative; do
    case "$relative" in
      */testdata/*|*/fixtures/*|*_test.go) continue ;;
      backend/*.go|backend/go.mod|backend/go.sum|backend/migrations/*.sql|backend/internal/pkg/openai/*.txt|backend/internal/pkg/openai/codex_gpt61_sol.json|backend/cmd/server/VERSION)
        copy_file "$relative" ;;
    esac
  done < <(git -C "$repo" ls-files -z -- backend)
fi
DOCKER_BUILDKIT=1 docker build --platform "$platform" \
  --target "$target" --file "$context/deploy/Dockerfile.cloud" --tag "$image" \
  --build-arg "BUILD_GOMAXPROCS=${BUILD_GOMAXPROCS:-1}" \
  --build-arg "BUILD_GOMEMLIMIT=${BUILD_GOMEMLIMIT:-1400MiB}" \
  --build-arg "BUILD_GOGC=${BUILD_GOGC:-20}" \
  "$context"
