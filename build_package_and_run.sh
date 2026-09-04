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

printf '[1/5] Installing locked frontend dependencies...\n'
(
  cd frontend
  npm ci
)

printf '[2/5] Building the embedded frontend...\n'
(
  cd frontend
  npm run build
)

printf '[3/5] Building the native application...\n'
STAGING="$ROOT/dist.staging"
rm -rf -- "$STAGING"
mkdir -p "$STAGING/CodeAtlas.app/Contents/MacOS" "$STAGING/CodeAtlas.app/Contents/Resources/bin"
cp packaging/macos/CodeAtlas.Info.plist "$STAGING/CodeAtlas.app/Contents/Info.plist"
cp packaging/macos/CodeAtlas.icns "$STAGING/CodeAtlas.app/Contents/Resources/CodeAtlas.icns"
(
  cd backend
  CGO_ENABLED=1 CC="$CC" CXX="$CXX" go build -tags 'fts5 desktop' -trimpath \
    -o "$STAGING/CodeAtlas.app/Contents/MacOS/codeatlas" ./cmd/codeatlas
)
printf '[4/5] Installing bundled gopls v0.23.0...\n'
(
  cd backend
  GOBIN="$STAGING/CodeAtlas.app/Contents/Resources/bin" GOTOOLCHAIN=auto go install golang.org/x/tools/gopls@v0.23.0
)
printf '[5/5] Installing bundled Node.js v24.20.0 runtime, pyright and typescript-language-server...\n'
(
  cd packaging/lsp
  npm ci --ignore-scripts --no-audit --no-fund
)
RESOURCES="$STAGING/CodeAtlas.app/Contents/Resources"
NODE_VERSION='v24.20.0'
case "$(uname -m)" in
  arm64) node_arch='arm64' ;;
  x86_64) node_arch='x64' ;;
  *) fail "Unsupported macOS architecture for the bundled Node.js runtime: $(uname -m)" ;;
esac
node_dist="node-$NODE_VERSION-darwin-$node_arch"
node_cache="$ROOT/.cache/node"
mkdir -p "$node_cache"
node_tarball="$node_cache/$node_dist.tar.gz"
node_shasums="$node_cache/SHASUMS256-$NODE_VERSION.txt"
node_base_url="${CODEATLAS_NODE_DIST_URL:-https://nodejs.org/dist}/$NODE_VERSION"
if [[ ! -f "$node_tarball" || ! -f "$node_shasums" ]]; then
  command -v curl >/dev/null 2>&1 || fail 'curl is required to download the pinned Node.js runtime.'
  curl -fsSL -o "$node_shasums" "$node_base_url/SHASUMS256.txt" || fail "Could not download $node_base_url/SHASUMS256.txt"
  curl -fsSL -o "$node_tarball" "$node_base_url/$node_dist.tar.gz" || fail "Could not download $node_base_url/$node_dist.tar.gz"
fi
expected_sha="$(awk -v name="$node_dist.tar.gz" '$2 == name { print $1 }' "$node_shasums")"
[[ -n "$expected_sha" ]] || fail "SHASUMS256.txt does not list $node_dist.tar.gz"
actual_sha="$(shasum -a 256 "$node_tarball" | awk '{ print $1 }')"
if [[ "$actual_sha" != "$expected_sha" ]]; then
  rm -f -- "$node_tarball" "$node_shasums"
  fail "Checksum mismatch for $node_dist.tar.gz (expected $expected_sha, got $actual_sha). The cached download was removed; rerun the build."
fi
node_extract="$STAGING/node-extract"
mkdir -p "$node_extract"
tar -xzf "$node_tarball" -C "$node_extract" "$node_dist/bin/node" "$node_dist/LICENSE"
cp "$node_extract/$node_dist/bin/node" "$RESOURCES/bin/node"
cp "$node_extract/$node_dist/LICENSE" "$RESOURCES/node-LICENSE"
rm -rf -- "$node_extract"
mkdir -p "$RESOURCES/lsp"
cp -R packaging/lsp/node_modules "$RESOURCES/lsp/node_modules"
cp packaging/lsp/package.json packaging/lsp/package-lock.json "$RESOURCES/lsp/"
for entry in pyright:pyright/index.js pyright-langserver:pyright/langserver.index.js typescript-language-server:typescript-language-server/lib/cli.mjs; do
  launcher="${entry%%:*}"
  script="${entry#*:}"
  [[ -f "$RESOURCES/lsp/node_modules/$script" ]] || fail "Bundled language server entry point for $launcher is missing: packaging/lsp/node_modules/$script"
  cp "packaging/macos/lsp-bin/$launcher" "$RESOURCES/bin/$launcher"
done
[[ -f "$RESOURCES/lsp/node_modules/typescript/lib/tsserver.js" ]] || fail 'Bundled TypeScript SDK is missing: packaging/lsp/node_modules/typescript/lib/tsserver.js'
for license in pyright typescript typescript-language-server; do
  cp "packaging/licenses/$license-LICENSE" "$RESOURCES/$license-LICENSE"
done
cp packaging/macos/codeatlas-server "$STAGING/codeatlas-server"
cp packaging/licenses/gopls-LICENSE "$RESOURCES/gopls-LICENSE"
chmod +x "$STAGING/CodeAtlas.app/Contents/MacOS/codeatlas" "$RESOURCES/bin/"* "$STAGING/codeatlas-server"
# Finder may drop a .DS_Store into dist/ while it is being removed, which makes a
# single rm -rf fail with "Directory not empty". Move the old package aside first
# so the fresh bundle always lands at dist/, then clean up the stale copy.
if [[ -e "$ROOT/dist" ]]; then
  stale="$ROOT/dist.stale.$$"
  mv -- "$ROOT/dist" "$stale"
  rm -rf -- "$stale" || rm -rf -- "$stale" || printf 'Warning: could not remove %s\n' "$stale" >&2
fi
mv -- "$STAGING" "$ROOT/dist"

printf 'Built %s\n' "$ROOT/dist/CodeAtlas.app"
if [[ "${_CODEATLAS_BUILD_ONLY:-0}" == 1 ]]; then
  exit 0
fi
printf 'Starting CodeAtlas...\n'
open -W "$ROOT/dist/CodeAtlas.app" --args "$@"
