@echo off
setlocal enabledelayedexpansion

cd /d "%~dp0"

:: Check Go
where go >nul 2>&1
if %errorlevel% neq 0 (
    echo Error: Go not installed. Get it from https://go.dev/dl/
    exit /b 1
)

:: Build if needed
if not exist rag-server.exe (
    echo Building rag-server...
    go build -o rag-server.exe ./cmd/server/
    if %errorlevel% neq 0 (
        echo Error: Build failed.
        exit /b 1
    )
)

:: Check if rebuild needed (go.mod newer than binary)
for %%A in (go.mod) do set MOD_TIME=%%~tA
for %%A in (rag-server.exe) do set BIN_TIME=%%~tA
if "!MOD_TIME!" gtr "!BIN_TIME!" (
    echo Rebuilding rag-server...
    go build -o rag-server.exe ./cmd/server/
)

:: Setup Python sidecar if local provider
findstr /c:"provider: \"local\"" config.yaml >nul 2>&1
if %errorlevel% equ 0 (
    where python >nul 2>&1
    if %errorlevel% neq 0 (
        echo Error: Python required for local embedding.
        exit /b 1
    )
    if not exist sidecar\.venv (
        echo Setting up Python sidecar...
        python -m venv sidecar\.venv
        sidecar\.venv\Scripts\pip install -q -r sidecar\requirements.txt
    )
)

:: Stop existing
if exist .rag-server.pid (
    set /p OLD_PID=<.rag-server.pid
    taskkill /PID !OLD_PID! /F >nul 2>&1
    del .rag-server.pid
)

:: Start server
start /b "" rag-server.exe
timeout /t 1 /nobreak >nul

:: Get PID of rag-server.exe
for /f "tokens=2" %%a in ('tasklist /fi "imagename eq rag-server.exe" /fo list ^| findstr "PID:"') do (
    set PID=%%a
)
echo !PID!> .rag-server.pid

:: Wait for health
set ATTEMPTS=0
:healthloop
if !ATTEMPTS! geq 30 (
    echo Warning: server started but health check not responding yet
    goto :done
)
curl -s http://127.0.0.1:8765/health >nul 2>&1
if %errorlevel% equ 0 (
    echo RAG server started ^(PID: !PID!^) at http://127.0.0.1:8765
    goto :done
)
set /a ATTEMPTS+=1
timeout /t 1 /nobreak >nul
goto :healthloop

:done
endlocal
