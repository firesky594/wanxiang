#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

if [ ! -d web/node_modules ]; then
  echo "Installing frontend dependencies..."
  cd web
  npm install
  cd ..
fi

echo "Starting backend..."
(cd server && go run ./cmd/wanxiang) &
backend_pid=$!

trap 'kill "$backend_pid" 2>/dev/null || true' EXIT

echo "Starting frontend..."
cd web
npm run dev

wait "$backend_pid"
