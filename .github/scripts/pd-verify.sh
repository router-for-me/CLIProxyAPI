#!/usr/bin/env bash
# Verify the fork against the upstream commit it is built on. Formatting and
# tests block only on problems the fork causes (see pd-verify.py); the build,
# capabilities and example config checks always block.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
python3 .github/scripts/pd-verify.py "$@"
go build -o test-output ./cmd/server
trap 'rm -f test-output' EXIT
./test-output --capabilities | python3 -c 'import json,sys; c=json.load(sys.stdin); assert c["schema_version"]==1 and c["config_check"] and "soonest-reset-first" in c["routing_strategies"] and c["routing_status_path"]=="/v8/management/routing/status" and c["safe_drain"] is True and c["drain_schema_version"]==1 and c["drain_status_path"]=="/v8/management/server/drain"'
./test-output --check-config --config config.example.yaml
