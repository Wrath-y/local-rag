#!/bin/bash
set -e
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

# Build Go binary
if ! command -v go &>/dev/null; then
    echo "Error: Go not installed. Get it from https://go.dev/dl/"
    exit 1
fi

if [ ! -f rag-server ] || [ go.mod -nt rag-server ]; then
    echo "Building rag-server..."
    go build -o rag-server ./cmd/server/
fi

# Setup Python sidecar if local provider
PROVIDER=$(grep -A1 'embedding:' config.yaml 2>/dev/null | grep 'provider:' | awk '{print $2}' | tr -d '"' || echo "local")
if [ "$PROVIDER" = "local" ]; then
    if ! command -v python3 &>/dev/null; then
        echo "Error: Python3 required for local embedding."
        exit 1
    fi
    if [ ! -d sidecar/.venv ]; then
        echo "Setting up Python sidecar..."
        python3 -m venv sidecar/.venv
        sidecar/.venv/bin/pip install -q -r sidecar/requirements.txt
    fi
fi

# Stop existing
if [ -f .rag-server.pid ]; then
    kill "$(cat .rag-server.pid)" 2>/dev/null || true
    rm -f .rag-server.pid
fi

# Start
./rag-server &
PID=$!
echo "$PID" > .rag-server.pid

# Wait for health
for i in $(seq 1 30); do
    if curl -s http://127.0.0.1:8765/health >/dev/null 2>&1; then
        echo "RAG server started (PID: $PID) at http://127.0.0.1:8765"
        exit 0
    fi
    sleep 1
done
echo "Warning: server started but health check not responding yet"
