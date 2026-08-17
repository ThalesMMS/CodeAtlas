@echo off
setlocal DisableDelayedExpansion

set "ROOT=%~dp0"

where go >nul 2>&1
if errorlevel 1 (
  echo Error: Go 1.23 or newer is required.>&2
  exit /b 1
)
set "GO_VERSION="
for /f "tokens=3" %%V in ('go version') do set "GO_VERSION=%%V"
if not defined GO_VERSION (
  echo Error: Could not parse the installed Go version.>&2
  exit /b 1
)
call :go_version_supported "%GO_VERSION%"
if errorlevel 1 (
  echo Error: Go 1.23 or newer is required. Found %GO_VERSION:go=%.>&2
  exit /b 1
)
where node >nul 2>&1
if errorlevel 1 (
  echo Error: Node.js 26 or newer is required.>&2
  exit /b 1
)
set "NODE_VERSION="
for /f "delims=" %%V in ('node --version') do if not defined NODE_VERSION set "NODE_VERSION=%%V"
if not defined NODE_VERSION (
  echo Error: Could not parse the installed Node.js version.>&2
  exit /b 1
)
call :node_version_supported "%NODE_VERSION%"
if errorlevel 1 (
  echo Error: Node.js 26 or newer is required. Found %NODE_VERSION:v=%.>&2
  exit /b 1
)
where npm >nul 2>&1
if errorlevel 1 (
  echo Error: npm ^>=11.16.0 and ^<12 is required.>&2
  exit /b 1
)
set "NPM_VERSION="
for /f "delims=" %%V in ('call npm --version') do if not defined NPM_VERSION set "NPM_VERSION=%%V"
if not defined NPM_VERSION (
  echo Error: Could not parse the installed npm version.>&2
  exit /b 1
)
call :npm_version_supported "%NPM_VERSION%"
if errorlevel 1 (
  echo Error: npm ^>=11.16.0 and ^<12 is required. Found %NPM_VERSION%.>&2
  exit /b 1
)
if defined CC (
  where "%CC%" >nul 2>&1
  if not errorlevel 1 goto compiler_found
  echo Error: The configured C compiler was not found: %CC%>&2
  exit /b 1
)
where gcc >nul 2>&1
if not errorlevel 1 (
  set "CC=gcc"
  goto compiler_found
)
where clang >nul 2>&1
if not errorlevel 1 (
  set "CC=clang"
  goto compiler_found
)
echo Error: A GCC or Clang C compiler available to cgo is required.>&2
exit /b 1

:compiler_found
pushd "%ROOT%" >nul
if errorlevel 1 (
  echo Error: Could not enter the CodeAtlas repository directory.>&2
  exit /b 1
)

if exist ".env" (
  for /f "usebackq eol=# tokens=1,* delims==" %%A in (".env") do set "%%A=%%B"
)
if not defined CODEATLAS_WORKSPACE set "CODEATLAS_WORKSPACE=%ROOT%examples\tinycommerce"

echo [1/3] Installing locked frontend dependencies...
cd /d "%ROOT%frontend"
call npm ci
if errorlevel 1 goto build_failed

echo [2/3] Building the embedded frontend...
call npm run build
if errorlevel 1 goto build_failed

echo [3/3] Building the native executable...
if not exist "%ROOT%dist" mkdir "%ROOT%dist"
cd /d "%ROOT%backend"
set "CGO_ENABLED=1"
call go build -tags fts5 -trimpath -o "%ROOT%dist\codeatlas.exe" .\cmd\codeatlas
if errorlevel 1 goto build_failed

cd /d "%ROOT%"
echo Built %ROOT%dist\codeatlas.exe
echo Starting CodeAtlas...
"%ROOT%dist\codeatlas.exe" %*
set "EXIT_CODE=%ERRORLEVEL%"
popd >nul
exit /b %EXIT_CODE%

:build_failed
set "EXIT_CODE=%ERRORLEVEL%"
if "%EXIT_CODE%"=="0" set "EXIT_CODE=1"
echo Packaging failed with exit code %EXIT_CODE%.>&2
popd >nul
exit /b %EXIT_CODE%

:go_version_supported
set "CHECK_VERSION=%~1"
set "CHECK_VERSION=%CHECK_VERSION:go=%"
for /f "tokens=1,2 delims=." %%A in ("%CHECK_VERSION%") do (
  if %%A GTR 1 exit /b 0
  if %%A EQU 1 if %%B GEQ 23 exit /b 0
)
exit /b 1

:node_version_supported
set "CHECK_VERSION=%~1"
set "CHECK_VERSION=%CHECK_VERSION:v=%"
for /f "tokens=1 delims=." %%A in ("%CHECK_VERSION%") do if %%A GEQ 26 exit /b 0
exit /b 1

:npm_version_supported
set "CHECK_VERSION=%~1"
for /f "tokens=1,2,3 delims=." %%A in ("%CHECK_VERSION%") do (
  if not %%A EQU 11 exit /b 1
  if %%B GTR 16 exit /b 0
  if %%B EQU 16 if %%C GEQ 0 exit /b 0
)
exit /b 1
