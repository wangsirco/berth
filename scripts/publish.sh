#!/bin/sh
# Copy the public tree into a checkout of github.com/wangsirco/berth and commit it there.
#   scripts/publish.sh ../berth-public "Berth 1.1.0"
# Paths in .publicignore are left out; the dev box's addresses and a private project's name used
# in tests and examples are replaced. This repository's own history stays private.
set -eu
DEST=${1:?usage: publish.sh <public checkout> [message]}
MSG=${2:-Berth}
SRC=$(git rev-parse --show-toplevel)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
git -C "$SRC" archive HEAD | tar -x -C "$TMP"
grep -v '^#' "$SRC/.publicignore" | while read -r p; do [ -n "$p" ] && rm -rf "${TMP:?}/$p"; done
# text files only: names and addresses that are not for the public
grep -rlI -e acme -e Acme -e Acme -e '10\.0\.2\.181' -e '10\.0\.2\.112' "$TMP" | while read -r f; do
  perl -pi -e 's/acme/acme/g; s/Acme/Acme/g; s/Acme/Acme/g; s/10\.0\.2\.181/192.0.2.10/g; s/10\.0\.2\.112/192.0.2.20/g' "$f"
done
if grep -rIl -i -e acme -e '10\.0\.2\.181' "$TMP" >/dev/null; then echo "private names left in the export" >&2; exit 1; fi
rsync -a --delete --exclude .git "$TMP/" "$DEST/"
cd "$DEST"
git add -A
git -c user.name="Collin Wang" -c user.email="101884559+wangsirco@users.noreply.github.com" commit -q -m "$MSG

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
git log --oneline -1
