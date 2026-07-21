#!/bin/bash
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

# Poll until the process is gone or max_tries is reached.
# Usage: wait_for_exit <pid> <max_tries> <interval_seconds>
wait_for_exit() {
    local pid="$1"
    local max_tries="$2"
    local interval="$3"
    local i=0
    while [ $i -lt "$max_tries" ]; do
        if ! kill -0 "$pid" 2>/dev/null; then
            return 0
        fi
        sleep "$interval"
        i=$((i + 1))
    done
    return 1
}

if [ -f .rag-server.pid ]; then
    PID=$(cat .rag-server.pid)

    # Check whether the process is actually alive (stale PID guard).
    if ! kill -0 "$PID" 2>/dev/null; then
        echo "Process $PID not running (stale PID file); cleaning up."
        rm -f .rag-server.pid
        exit 0
    fi

    # Send SIGTERM and wait up to 6 seconds (60 polls × 0.1 s).
    kill -TERM "$PID" 2>/dev/null
    if wait_for_exit "$PID" 60 0.1; then
        echo "RAG server stopped (PID: $PID)"
        rm -f .rag-server.pid
        exit 0
    fi

    # Graceful shutdown timed out – escalate to SIGKILL.
    echo "RAG server (PID: $PID) did not exit within 6 seconds; sending SIGKILL."
    kill -KILL "$PID" 2>/dev/null
    if wait_for_exit "$PID" 60 0.1; then
        echo "RAG server force-killed (PID: $PID)"
        rm -f .rag-server.pid
        exit 0
    fi

    echo "ERROR: RAG server (PID: $PID) could not be killed; leaving PID file intact."
    exit 1
else
    # No PID file – try a targeted pattern match to avoid killing unrelated processes.
    if pkill -f "[/.]rag-server" 2>/dev/null; then
        echo "RAG server stopped (matched by name)."
    else
        echo "No running RAG server found."
    fi
fi
