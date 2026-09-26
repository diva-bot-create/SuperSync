#!/bin/bash
# Builds SuperSync for release into dist/:
#   SuperSync.dmg            Mac installer (drag SuperSync.app to Applications)
#   SuperSync-mac.zip        SuperSync.app, for the self-updater
#   SuperSync-Setup.exe      Windows installer
#   SuperSync-windows.zip    SuperSync.exe, for the self-updater
#   supersync-linux-*        command-line builds for Linux
# Needs macOS with Xcode's command line tools (the Mac app window uses WebKit
# through cgo) and NSIS (brew install makensis) for the Windows installer.
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:-$(git describe --tags --always 2>/dev/null || echo dev)}"
PLAIN="${VERSION#v}"
LDFLAGS="-s -w -X main.version=$VERSION"
rm -rf dist && mkdir -p dist
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

echo "Building SuperSync $VERSION"

# ---- macOS: a universal SuperSync.app ----
export MACOSX_DEPLOYMENT_TARGET=11.0 CGO_CFLAGS="-mmacosx-version-min=11.0" CGO_LDFLAGS="-mmacosx-version-min=11.0"
for arch in arm64 amd64; do
  carch=$arch; [ $arch = amd64 ] && carch=x86_64
  CGO_ENABLED=1 GOOS=darwin GOARCH=$arch CC="clang -arch $carch" \
    go build -trimpath -ldflags "$LDFLAGS" -o "$work/mac-$arch" .
done
app="$work/SuperSync.app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
lipo -create -output "$app/Contents/MacOS/SuperSync" "$work/mac-arm64" "$work/mac-amd64"
sed "s/__VERSION__/$PLAIN/g" scripts/macos/Info.plist > "$app/Contents/Info.plist"
cp assets/SuperSync.icns "$app/Contents/Resources/"
printf 'APPL????' > "$app/Contents/PkgInfo"
codesign --force --deep --sign - "$app" # ad-hoc: Apple Silicon needs a signature (it's not notarized)
(cd "$work" && ditto -c -k --keepParent SuperSync.app "$OLDPWD/dist/SuperSync-mac.zip")
echo "  SuperSync-mac.zip"

dmg="$work/dmg"
mkdir -p "$dmg"
cp -R "$app" "$dmg/"
ln -s /Applications "$dmg/Applications"
hdiutil create -quiet -volname "SuperSync" -srcfolder "$dmg" -ov -format UDZO dist/SuperSync.dmg
echo "  SuperSync.dmg"

# ---- Windows: a windowed SuperSync.exe with its icon, and an installer ----
go run github.com/tc-hib/go-winres@v0.3.3 make --in scripts/windows/winres.json \
  --out "$work/rsrc" --arch amd64,arm64 --product-version "$PLAIN.0" --file-version "$PLAIN.0" >/dev/null
cp "$work"/rsrc_windows_*.syso .
trap 'rm -rf "$work"; rm -f rsrc_windows_*.syso' EXIT
win="$work/win"
mkdir -p "$win"
# Not stripped (-s -w): stripped, unsigned executables score worse with
# antivirus heuristics, and the size difference doesn't matter.
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-X main.version=$VERSION -H windowsgui" -o "$win/SuperSync.exe" .
rm -f rsrc_windows_*.syso
cp assets/SuperSync.ico "$win/"
(cd "$win" && zip -q "$OLDPWD/dist/SuperSync-windows.zip" SuperSync.exe)
echo "  SuperSync-windows.zip"
# (makensis from Homebrew crashes without a UTF-8 locale.)
if command -v makensis >/dev/null; then
  LC_ALL=en_US.UTF-8 LANG=en_US.UTF-8 makensis -V2 -DVERSION="$VERSION" -DSRC="$win" -DOUT="$PWD/dist/SuperSync-Setup.exe" scripts/windows/installer.nsi
  echo "  SuperSync-Setup.exe"
else
  echo "  (skipped SuperSync-Setup.exe: install NSIS with 'brew install makensis')"
fi

# ---- Linux: command-line builds (no app window) ----
for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath -ldflags "$LDFLAGS" -o "dist/supersync-linux-$arch" .
done
echo "  supersync-linux-amd64, -arm64"

ls -lh dist
