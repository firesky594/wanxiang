#!/usr/bin/env bash

set -euo pipefail

server_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$server_root"

exec go run ./test/runner "$@"
