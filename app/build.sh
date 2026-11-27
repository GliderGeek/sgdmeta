#!/bin/sh
# Builds the app for macOS (universal .app) and Windows (x64 .exe) into dist/.
# Pure Go (no cgo), so everything cross-compiles, from macOS or Linux.
#
# Needs: Go and zip. The universal macOS binary is made with makefat (a Go
# tool pinned in go.mod); the icon is the committed icon.icns (make-icns.sh).
set -eu
cd "$(dirname "$0")"

NAME=sgdmeta
APP=sgdmeta
VERSION=${VERSION:-0.1.0}
export CGO_ENABLED=0

DIST=dist
BUILD=build
rm -rf "$DIST" "$BUILD" && mkdir -p "$DIST" "$BUILD"

# ------------------------------------------------------------ macOS
echo "macOS (arm64 + amd64)..."
GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags "-s -w" -o $BUILD/$NAME-arm64 .
GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o $BUILD/$NAME-amd64 .

BUNDLE=$DIST/$APP.app
mkdir -p "$BUNDLE/Contents/MacOS" "$BUNDLE/Contents/Resources"
go tool makefat "$BUNDLE/Contents/MacOS/$NAME" $BUILD/$NAME-amd64 $BUILD/$NAME-arm64
cp icon.icns "$BUNDLE/Contents/Resources/icon.icns"

# LSUIElement: no Dock icon; the UI lives in the browser and the program
# stops by itself.
cat > "$BUNDLE/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleName</key><string>$APP</string>
	<key>CFBundleDisplayName</key><string>$APP</string>
	<key>CFBundleExecutable</key><string>$NAME</string>
	<key>CFBundleIdentifier</key><string>nl.sgdmeta</string>
	<key>CFBundleIconFile</key><string>icon</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleShortVersionString</key><string>$VERSION</string>
	<key>CFBundleVersion</key><string>$VERSION</string>
	<key>CFBundleDevelopmentRegion</key><string>nl</string>
	<key>LSMinimumSystemVersion</key><string>12.0</string>
	<key>LSUIElement</key><true/>
</dict>
</plist>
EOF

(cd "$DIST" && zip -qry $APP-mac.zip $APP.app)

# ------------------------------------------------------------ Windows
echo "Windows (x64)..."
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w -H windowsgui" -o $DIST/$NAME.exe .

rm -rf "$BUILD"
echo
ls -lh "$DIST"
file "$BUNDLE/Contents/MacOS/$NAME" $DIST/$NAME.exe
