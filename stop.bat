@echo off
setlocal enabledelayedexpansion

cd /d "%~dp0"

if exist .rag-server.pid (
    set /p PID=<.rag-server.pid
    taskkill /PID !PID! /F >nul 2>&1
    if %errorlevel% equ 0 (
        echo RAG server stopped ^(PID: !PID!^)
    ) else (
        echo Process not running
    )
    del .rag-server.pid
) else (
    taskkill /im rag-server.exe /F >nul 2>&1
    if %errorlevel% equ 0 (
        echo RAG server stopped
    ) else (
        echo No running server found
    )
)

endlocal
