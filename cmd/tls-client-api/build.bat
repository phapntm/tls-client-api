@echo off
setlocal

set "VERSION="
set "TLS_CLIENT_GO_MOD=%~dp0..\..\go.mod"
for /f "delims=" %%V in ('powershell -NoProfile -Command "$ErrorActionPreference = 'Stop'; $json = go mod edit -json $env:TLS_CLIENT_GO_MOD; if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }; $module = ConvertFrom-Json -InputObject ($json -join [Environment]::NewLine); foreach ($dependency in $module.Require) { if ($dependency.Path -eq 'github.com/bogdanfinn/tls-client') { $dependency.Version; break } }"') do set "VERSION=%%V"
if not defined VERSION (
    echo ERROR: Khong doc duoc version github.com/bogdanfinn/tls-client trong go.mod.
    exit /b 1
)

echo.
echo TLS-Client version: %VERSION%
echo Chon ban build:
echo   1. macOS ARM64
echo   2. macOS AMD64
echo   3. Linux AMD64
echo   4. Windows 32 Bit
echo   5. Windows 64 Bit
echo   6. Build all
choice /c 123456 /n /m "Nhap lua chon [1-6]: "
set "TARGET=%ERRORLEVEL%"
if "%TARGET%"=="0" exit /b 1
if "%TARGET%"=="255" exit /b 1

pushd "%~dp0" || exit /b 1
set "BUILD_EXIT_CODE=0"

if "%TARGET%"=="6" (
    for %%T in (1 2 3 4 5) do (
        call :build_target %%T
        if errorlevel 1 goto build_failed
    )
) else (
    call :build_target %TARGET%
    if errorlevel 1 goto build_failed
)
goto finish

:build_target
set "GOOS=darwin"
set "GOARCH=arm64"
set "OUTPUT=tls-client-api-darwin-arm64-%VERSION%"

if "%~1"=="2" (
    set "GOARCH=amd64"
    set "OUTPUT=tls-client-api-darwin-amd64-%VERSION%"
)

if "%~1"=="3" (
    set "GOOS=linux"
    set "GOARCH=amd64"
    set "OUTPUT=tls-client-api-linux-amd64-%VERSION%"
)

if "%~1"=="4" (
    set "GOOS=windows"
    set "GOARCH=386"
    set "OUTPUT=tls-client-api-windows-32-%VERSION%.exe"
)

if "%~1"=="5" (
    set "GOOS=windows"
    set "GOARCH=amd64"
    set "OUTPUT=tls-client-api-windows-64-%VERSION%.exe"
)

echo.
echo Build %GOOS%/%GOARCH%
go build -o "./../../dist/%OUTPUT%" ./main.go
exit /b %ERRORLEVEL%

:build_failed
set "BUILD_EXIT_CODE=%ERRORLEVEL%"

:finish
popd
exit /b %BUILD_EXIT_CODE%
