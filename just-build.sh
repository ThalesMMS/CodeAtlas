#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"

if (($# > 0)); then
  printf 'Usage: %s\n' "$(basename -- "$0")" >&2
  exit 2
fi

_CODEATLAS_BUILD_ONLY=1 exec "$ROOT/build_package_and_run.sh"
