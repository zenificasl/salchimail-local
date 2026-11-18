#!/bin/sh
# Builds salchimail-local for every supported platform into the given directory
# (./dist by default), with their SHA-256 sums. No code signing.
set -eu
cd "$(dirname "$0")"
out="${1:-dist}"
mkdir -p "$out"
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  os=${target%/*}
  arch=${target#*/}
  ext=""
  [ "$os" = windows ] && ext=".exe"
  # No cgo, paths or VCS stamp: the same source and Go version give the same binary.
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -buildvcs=false -ldflags "-s -w" -o "$out/salchimail-local-$os-$arch$ext" .
done
cd "$out"
if command -v sha256sum >/dev/null; then
  sha256sum salchimail-local-* > SHA256SUMS
else
  shasum -a 256 salchimail-local-* > SHA256SUMS # macOS
fi
