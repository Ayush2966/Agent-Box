#!/bin/sh
# usage: mkapp.sh <binary> <out.app> [version]  — assembles a minimal macOS bundle.
set -eu
bin=$1; app=$2; version=${3:-dev}
rm -rf "$app"
mkdir -p "$app/Contents/MacOS"
cp "$bin" "$app/Contents/MacOS/agentbox-desktop"
chmod +x "$app/Contents/MacOS/agentbox-desktop"
sed "s/__VERSION__/$version/g" "$(dirname "$0")/Info.plist.tmpl" > "$app/Contents/Info.plist"
echo "built $app"
