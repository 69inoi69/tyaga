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
if ! echo "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
  echo "The version must look like 1.2.0 (or v1.2.0), got: $1" >&2
  exit 1
fi
major=${version%%.*}
rest=${version#*.}
minor=${rest%%.*}
patch=${rest#*.}

# llvm-windres, llvm-windres-18 or /usr/lib/llvm-18/bin/llvm-windres: whichever exists, newest first.
windres=$(command -v llvm-windres ||
  ls /usr/bin/llvm-windres-* /usr/lib/llvm-*/bin/llvm-windres 2>/dev/null | sort -V | tail -1 || true)
if [ -z "$windres" ]; then
  echo "llvm-windres not found. Install it: sudo apt install llvm" >&2
  exit 1
fi
echo "Using $windres for version $version"

sed -e "s/@MAJOR@/$major/g; s/@MINOR@/$minor/g; s/@PATCH@/$patch/g; s/@VERSION@/$version/g" tyaga.rc.in > tyaga.rc
trap 'rm -f tyaga.rc' EXIT
# --no-preprocess: the script needs no C preprocessor, and so no clang on the machine.
for arch in amd64:x86_64 arm64:aarch64; do
  "$windres" --no-preprocess --target="${arch#*:}-w64-mingw32" -O coff tyaga.rc "../rsrc_windows_${arch%%:*}.syso"
done
