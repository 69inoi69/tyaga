#!/bin/sh
# Builds the Windows resources (icon + version info) into .syso files that "go build"
# picks up by itself for windows/amd64 and windows/arm64. Needs llvm-windres
# (apt install llvm). The .syso files are committed, so a normal build doesn't need this;
# the release workflow reruns it to stamp the real version.
#   winres/make.sh 1.2.0
set -eu
cd "$(dirname "$0")"
version=${1:-0.0.0}
version=${version#v}
IFS=. read -r major minor patch <<END
$version
END
windres=$(command -v llvm-windres || ls /usr/bin/llvm-windres-* 2>/dev/null | sort -V | tail -1)
sed -e "s/@MAJOR@/$major/g; s/@MINOR@/$minor/g; s/@PATCH@/$patch/g; s/@VERSION@/$version/g" tyaga.rc.in > tyaga.rc
for arch in amd64:x86_64 arm64:aarch64; do
  "$windres" --target="${arch#*:}-w64-mingw32" -O coff tyaga.rc "../rsrc_windows_${arch%%:*}.syso"
done
rm tyaga.rc
