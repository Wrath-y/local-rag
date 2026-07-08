#!/bin/bash
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

if [ -f .rag-server.pid ]; then
    PID=$(cat .rag-server.pid)
    kill "$PID" 2>/dev/null && echo "RAG server stopped (PID: $PID)" || echo "Process not running"
    rm -f .rag-server.pid
else
    pkill -f rag-server 2>/dev/null && echo "RAG server stopped" || echo "No running server found"
fi
