#!/bin/sh
# Homebrew cask for the desktop app, written from desktop/agentbox-desktop.rb.tmpl.
#
#   publish-cask.sh render  <zip> <version>          prints the cask
#   publish-cask.sh publish <zip> <version> [tap]    commits it to the tap
#
# <zip> is the release asset Agentbox-<version>-macos-universal.zip (the cask's
# url names it, so the file name is checked); <version> is the tag without "v".
# publish writes Casks/agentbox-desktop.rb through the GitHub contents API, so
# GH_TOKEN must be allowed to write the tap (default Ayush2966/homebrew-tap).
set -eu
usage() { echo "usage: $0 render|publish <zip> <version> [owner/tap-repo]" >&2; exit 2; }
[ $# -ge 3 ] || usage
cmd=$1; zip=$2; version=$3; tap=${4:-Ayush2966/homebrew-tap}
here=$(dirname "$0")

case $version in
  *[!0-9.]*|.*|*.|*..*|"") echo "version must be dotted digits, got '$version'" >&2; exit 2 ;;
esac
[ -f "$zip" ] || { echo "no such file: $zip" >&2; exit 2; }
want="Agentbox-$version-macos-universal.zip"
[ "$(basename "$zip")" = "$want" ] || { echo "zip must be named $want (the cask url), got $(basename "$zip")" >&2; exit 2; }

sha=$(shasum -a 256 "$zip" | cut -d' ' -f1)
cask=$(sed -e "s/__VERSION__/$version/g" -e "s/__SHA256__/$sha/g" "$here/agentbox-desktop.rb.tmpl")

case $cmd in
  render)
    printf '%s\n' "$cask" ;;
  publish)
    path=Casks/agentbox-desktop.rb
    existing=$(gh api "repos/$tap/contents/$path" --jq .sha 2>/dev/null || true)
    content=$(printf '%s\n' "$cask" | base64 | tr -d '\n')
    set -- -X PUT "repos/$tap/contents/$path" \
      -f "message=Brew cask update for agentbox-desktop version v$version" \
      -f "content=$content"
    [ -n "$existing" ] && set -- "$@" -f "sha=$existing"
    gh api "$@" --jq '.commit.html_url' ;;
  *) usage ;;
esac
