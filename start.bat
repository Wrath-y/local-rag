@echo off
setlocal
cd /d "%~dp0"
rem 生命周期状态和进程身份由跨平台 Go 入口统一处理。
set "BIN=%~dp0.run\bin\rag-server.exe"
if exist "%BIN%" goto :run
where go >nul 2>&1
if errorlevel 1 (
    echo 缺少 Go，请先安装与 go.mod 匹配的 Go 工具链。 1>&2
    exit /b 1
)
if not exist "%~dp0.run\bin" mkdir "%~dp0.run\bin"
go build -o "%BIN%" ./cmd/server/
if errorlevel 1 exit /b 1
:run
"%BIN%" lifecycle start %*
exit /b %errorlevel%
