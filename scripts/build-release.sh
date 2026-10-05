#!/usr/bin/env bash
# ============================================================================
# build-release.sh — 构建发布用二进制与 checksums.txt（release 工作流与本地共用）
# 用法（cli/ 目录）：bash scripts/build-release.sh <版本 tag> <输出目录>
# ============================================================================
set -euo pipefail

VERSION="${1:?用法: build-release.sh <版本 tag> <输出目录>}"
OUT="${2:?用法: build-release.sh <版本 tag> <输出目录>}"
mkdir -p "$OUT"

for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
  goos="${target%/*}" goarch="${target#*/}"
  name="dsync_${goos}_${goarch}"
  [ "$goos" = windows ] && name="$name.exe"
  GOOS="$goos" GOARCH="$goarch" CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/Lin-Ya/dsync-cli/internal/cli.Version=$VERSION" \
    -o "$OUT/$name" ./cmd/dsync
  echo "built $OUT/$name"
done
(cd "$OUT" && sha256sum dsync_* > checksums.txt)
cat "$OUT/checksums.txt"
