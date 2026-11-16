#!/bin/sh
# Validate the renderer's all-features sample with a real Caddy on a dev host.
# Usage: scripts/caddy-validate.sh root@host   (the host needs the berth/caddy image)
set -eu
HOST=$1
TMP=$(mktemp -d)
BERTH_CADDY_SAMPLE=$TMP/sample.json go test ./internal/panel/caddycfg/ -run TestWriteSample -count=1 >/dev/null
scp -q "$TMP/sample.json" "$HOST:/tmp/berth-sample.json"
ssh "$HOST" 'img=$(docker images --format "{{.Repository}}:{{.Tag}}" berth/caddy | head -1); docker run --rm -v /tmp/berth-sample.json:/s.json:ro "$img" caddy validate --config /s.json 2>&1 | tail -1'
rm -rf "$TMP"
