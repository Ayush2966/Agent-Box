#!/bin/sh
# usage: mkapp.sh <binary> <out.app> [version]  — assembles a minimal macOS bundle.
# version must be numeric dotted (CFBundleVersion rules): the Makefile passes the
# tag without its "v", or 0.0.0 for an untagged build.
set -eu
bin=$1; app=$2; version=${3:-0.0.0}
here=$(dirname "$0")
rm -rf "$app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
cp "$bin" "$app/Contents/MacOS/agentbox-desktop"
chmod +x "$app/Contents/MacOS/agentbox-desktop"
cp "$here/agentbox.icns" "$app/Contents/Resources/agentbox.icns"
sed "s/__VERSION__/$version/g" "$here/Info.plist.tmpl" > "$app/Contents/Info.plist"
echo "built $app ($version)"
