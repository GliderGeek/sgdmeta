#!/bin/sh
# Regenerates icon.icns from icon.png. macOS only (sips and iconutil ship
# with macOS). Run this after changing icon.png and commit icon.icns; build.sh
# only copies it, so the release build also works on Linux.
set -eu
cd "$(dirname "$0")"

ICONSET=$(mktemp -d)/icon.iconset
mkdir -p "$ICONSET"
for s in 16 32 128 256 512; do
	sips -z $s $s icon.png --out "$ICONSET/icon_${s}x${s}.png" >/dev/null
	d=$((s * 2))
	sips -z $d $d icon.png --out "$ICONSET/icon_${s}x${s}@2x.png" >/dev/null
done
iconutil -c icns "$ICONSET" -o icon.icns
rm -rf "$(dirname "$ICONSET")"
echo "icon.icns bijgewerkt"
