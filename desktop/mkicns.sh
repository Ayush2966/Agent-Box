#!/bin/sh
# usage: mkicns.sh <icon-1024.png> <out.icns> — builds the .icns Finder and the Dock use (macOS only).
set -eu
src=$1; out=$2
set=$(mktemp -d)/agentbox.iconset
mkdir -p "$set"
for s in 16 32 128 256 512; do
  sips -z "$s" "$s" "$src" --out "$set/icon_${s}x${s}.png" >/dev/null
  d=$((s * 2))
  sips -z "$d" "$d" "$src" --out "$set/icon_${s}x${s}@2x.png" >/dev/null
done
iconutil -c icns "$set" -o "$out"
rm -rf "$(dirname "$set")"
echo "built $out"
