#!/bin/sh
# Build a release: the binaries (arm64 for now; ARCHES="arm64 amd64" once x86 is ready), a web
# bundle, and a signed manifest.
#   BERTH_SIGN_SEED=<base64 seed> scripts/release.sh 0.1.0
# The seed never touches the disk here; keep it in a password manager.
set -eu
VER=${1:?usage: release.sh VERSION}
: "${BERTH_SIGN_SEED:?set BERTH_SIGN_SEED (base64 ed25519 seed)}"
OUT=dist-release/$VER
PUB=$(printf %s "$BERTH_SIGN_SEED" | go run ./scripts/pubkey)
rm -rf "$OUT" && mkdir -p "$OUT"
(cd web && npm ci --silent && npm run build --silent)
read -r WEB CORE <<EOH
$(go run ./scripts/hashes)
EOH
LD="-s -w -X github.com/wangsirco/berth/internal/version.Version=$VER -X github.com/wangsirco/berth/internal/version.ReleasePubKey=$PUB -X github.com/wangsirco/berth/internal/version.CoreHash=$CORE"
for ARCH in ${ARCHES:-arm64}; do
  D=$(mktemp -d)/berth_${VER}
  mkdir -p "$D"
  CGO_ENABLED=0 GOOS=linux GOARCH=$ARCH go build -trimpath -ldflags "$LD" -o "$D/berth" ./cmd/berth
  CGO_ENABLED=0 GOOS=linux GOARCH=$ARCH go build -trimpath -ldflags "$LD" -o "$D/berth-panel" ./cmd/berth-panel
  cp packaging/berth-agent.service packaging/berth-panel.service scripts/install.sh LICENSE NOTICE "$D/"
  COPYFILE_DISABLE=1 tar --no-xattrs -C "$(dirname "$D")" -czf "$OUT/berth_${VER}_linux_${ARCH}.tar.gz" "berth_${VER}"
done
COPYFILE_DISABLE=1 tar --no-xattrs -C web/dist -czf "$OUT/berth_${VER}_web.tar.gz" .
cp scripts/install.sh "$OUT/install.sh"
go run ./scripts/mkmanifest -dir "$OUT" -version "$VER" -web-hash "$WEB" -core-hash "$CORE" ${NOTES:+-notes "$NOTES"}
( cd "$OUT" && shasum -a 256 ./*.tar.gz > SHA256SUMS )
echo "release $VER in $OUT (public key $PUB)"
