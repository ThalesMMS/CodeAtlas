#!/usr/bin/env bash
set -Eeuo pipefail

ROOT="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"

fail() {
  printf 'Error: %s\n' "$1" >&2
  exit 1
}

version_at_least() {
  local major="$1" minor="$2" patch="$3"
  local required_major="$4" required_minor="$5" required_patch="$6"
  ((major > required_major)) ||
    ((major == required_major && minor > required_minor)) ||
    ((major == required_major && minor == required_minor && patch >= required_patch))
}

load_dotenv() {
  local file="$1" line='' key='' value='' line_number=0
  while IFS= read -r line || [[ -n "$line" ]]; do
    ((line_number += 1))
    line="${line%$'\r'}"
    [[ -z "$line" || "$line" == \#* ]] && continue
    if [[ ! "$line" =~ ^([A-Za-z_][A-Za-z0-9_]*)=(.*)$ ]]; then
      fail "Invalid .env entry on line $line_number. Expected KEY=VALUE."
    fi
    key="${BASH_REMATCH[1]}"
    value="${BASH_REMATCH[2]}"
    [[ "$key" == CODEATLAS_* ]] || fail "Unsupported .env variable on line $line_number: $key. Only CODEATLAS_* variables are allowed."
    printf -v "$key" '%s' "$value"
    export "$key"
  done < "$file"
}

host_os="$(uname -s)"
[[ "$host_os" == Darwin ]] || fail "macOS packaging requires Darwin. Found $host_os."

command -v go >/dev/null 2>&1 || fail 'Go 1.23 or newer is required.'
command -v node >/dev/null 2>&1 || fail 'Node.js 26 or newer is required.'
command -v npm >/dev/null 2>&1 || fail 'npm >=11.16.0 and <12 is required.'

go_version_output="$(go version)"
[[ "$go_version_output" =~ go([0-9]+)\.([0-9]+)(\.([0-9]+))? ]] || fail "Could not parse the installed Go version: $go_version_output"
go_version="${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.${BASH_REMATCH[4]:-0}"
version_at_least "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}" "${BASH_REMATCH[4]:-0}" 1 23 0 || fail "Go 1.23 or newer is required. Found $go_version."

node_version_output="$(node --version)"
[[ "$node_version_output" =~ ^v?([0-9]+)\.([0-9]+)\.([0-9]+) ]] || fail "Could not parse the installed Node.js version: $node_version_output"
node_version="${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.${BASH_REMATCH[3]}"
version_at_least "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}" "${BASH_REMATCH[3]}" 26 0 0 || fail "Node.js 26 or newer is required. Found $node_version."

npm_version_output="$(npm --version)"
[[ "$npm_version_output" =~ ^([0-9]+)\.([0-9]+)\.([0-9]+) ]] || fail "Could not parse the installed npm version: $npm_version_output"
npm_version="${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.${BASH_REMATCH[3]}"
if ! version_at_least "${BASH_REMATCH[1]}" "${BASH_REMATCH[2]}" "${BASH_REMATCH[3]}" 11 16 0 || ((BASH_REMATCH[1] >= 12)); then
  fail "npm >=11.16.0 and <12 is required. Found $npm_version."
fi

if [[ -n "${CC:-}" ]]; then
  command -v "$CC" >/dev/null 2>&1 || fail "The configured C compiler was not found: $CC"
  if [[ -z "${CXX:-}" ]]; then
    case "$(basename -- "$CC")" in
      cc) CXX='c++' ;;
      clang) CXX='clang++' ;;
      gcc) CXX='g++' ;;
      *) fail "Set CXX to the C++ compiler matching the configured C compiler: $CC" ;;
    esac
  fi
elif [[ -n "${CXX:-}" ]]; then
  command -v "$CXX" >/dev/null 2>&1 || fail "The configured C++ compiler was not found: $CXX"
  case "$(basename -- "$CXX")" in
    c++) CC='cc' ;;
    clang++) CC='clang' ;;
    g++) CC='gcc' ;;
    *) fail "Set CC to the C compiler matching the configured C++ compiler: $CXX" ;;
  esac
else
  for pair in 'cc:c++' 'clang:clang++' 'gcc:g++'; do
    compiler="${pair%%:*}"
    cxx_compiler="${pair#*:}"
    if command -v "$compiler" >/dev/null 2>&1 && command -v "$cxx_compiler" >/dev/null 2>&1; then
      CC="$compiler"
      CXX="$cxx_compiler"
      break
    fi
  done
fi

[[ -n "${CC:-}" ]] || fail 'A C and C++ compiler pair is required. On macOS, install the Xcode Command Line Tools with: xcode-select --install'
[[ -n "${CXX:-}" ]] || fail "A C++ compiler matching $CC is required."
command -v "$CC" >/dev/null 2>&1 || fail "The configured C compiler was not found: $CC"
command -v "$CXX" >/dev/null 2>&1 || fail "The configured C++ compiler was not found: $CXX"
export CC CXX

cd "$ROOT"

if [[ -f .env ]]; then
  load_dotenv .env
fi

if [[ -z "${CODEATLAS_WORKSPACE:-}" ]]; then
  export CODEATLAS_WORKSPACE="$ROOT/examples/tinycommerce"
fi

printf '[1/3] Installing locked frontend dependencies...\n'
(
  cd frontend
  npm ci
)

printf '[2/3] Building the embedded frontend...\n'
(
  cd frontend
  npm run build
)

printf '[3/3] Building the native application...\n'
STAGING="$ROOT/dist.staging"
rm -rf -- "$STAGING"
mkdir -p "$STAGING/CodeAtlas.app/Contents/MacOS" "$STAGING/CodeAtlas.app/Contents/Resources"
cp packaging/macos/CodeAtlas.Info.plist "$STAGING/CodeAtlas.app/Contents/Info.plist"
(
  cd backend
  CGO_ENABLED=1 CC="$CC" CXX="$CXX" go build -tags 'fts5 desktop' -trimpath \
    -o "$STAGING/CodeAtlas.app/Contents/MacOS/codeatlas" ./cmd/codeatlas
)
cp packaging/macos/codeatlas-server "$STAGING/codeatlas-server"
chmod +x "$STAGING/CodeAtlas.app/Contents/MacOS/codeatlas" "$STAGING/codeatlas-server"
rm -rf -- "$ROOT/dist"
mv -- "$STAGING" "$ROOT/dist"

printf 'Built %s\n' "$ROOT/dist/CodeAtlas.app"
printf 'Starting CodeAtlas...\n'
open -W "$ROOT/dist/CodeAtlas.app" --args "$@"
