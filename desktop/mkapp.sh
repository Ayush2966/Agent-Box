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
# Sign the bundle. Ad-hoc by default: a sealed bundle with a stable identity
# (Gatekeeper still asks on first launch). CODESIGN_IDENTITY="Developer ID
# Application: ..." signs for real with the options notarization needs; that
# path has not been exercised (no certificate here) and may want entitlements.
identity=${CODESIGN_IDENTITY:--}
if [ "$identity" = "-" ]; then
  codesign --force --sign - "$app"
else
  codesign --force --sign "$identity" --options runtime --timestamp "$app"
fi
codesign --verify --strict "$app"
echo "built $app ($version, signed: $identity)"
