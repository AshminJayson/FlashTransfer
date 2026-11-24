#!/usr/bin/env bash
# Builds libusb as a universal (arm64 + x86_64) dylib to bundle inside FlashTransfer.app,
# so users need neither Homebrew nor a libusb install. Dynamic linking keeps the LGPL simple:
# the bundled library can be swapped for any compatible build.
#
# Output (gitignored): build/darwin/libusb/
#   lib/libusb-1.0.0.dylib   universal, install name @rpath/libusb-1.0.0.dylib
#   lib/pkgconfig/           for cgo to compile and link against it
#   include/                 headers
#   COPYING                  LGPL-2.1 text, shipped inside the app
set -euo pipefail

VERSION=1.0.30
# same tarball and checksum as Homebrew's libusb formula
SHA256=fea36f34f9156400209595e300840767ab1a385ede1dc7ee893015aea9c6dbaf
MIN_MACOS=13.0

out="$(cd "$(dirname "$0")" && pwd)/libusb"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

echo "Downloading libusb $VERSION"
curl -fsSL -o "$work/src.tar.bz2" \
  "https://github.com/libusb/libusb/releases/download/v$VERSION/libusb-$VERSION.tar.bz2"
echo "$SHA256  $work/src.tar.bz2" | shasum -a 256 -c -

for arch in arm64 x86_64; do
  host=$([ "$arch" = arm64 ] && echo aarch64-apple-darwin || echo x86_64-apple-darwin)
  echo "Building libusb for $arch"
  mkdir -p "$work/src-$arch"
  tar -xjf "$work/src.tar.bz2" -C "$work/src-$arch" --strip-components 1
  (
    cd "$work/src-$arch"
    flags="-arch $arch -mmacosx-version-min=$MIN_MACOS"
    # the build SDK may be newer than MIN_MACOS: pipe2 only exists from macOS 27, and any
    # other API newer than MIN_MACOS must fail the build rather than crash older Macs
    ac_cv_func_pipe2=no \
    CFLAGS="$flags -O2 -Werror=unguarded-availability-new" \
    LDFLAGS="$flags" \
      ./configure --host="$host" --prefix="$work/prefix-$arch" \
        --disable-static --disable-dependency-tracking >/dev/null
    make -j"$(sysctl -n hw.ncpu)" >/dev/null
    make install >/dev/null
  )
done

rm -rf "$out"
mkdir -p "$out/lib"
cp -R "$work/prefix-arm64/include" "$out/"
cp -R "$work/prefix-arm64/lib/pkgconfig" "$out/lib/"
lipo -create -output "$out/lib/libusb-1.0.0.dylib" \
  "$work/prefix-arm64/lib/libusb-1.0.0.dylib" "$work/prefix-x86_64/lib/libusb-1.0.0.dylib"
ln -s libusb-1.0.0.dylib "$out/lib/libusb-1.0.dylib"
# the app finds it in Contents/Frameworks via the binary's rpath
install_name_tool -id @rpath/libusb-1.0.0.dylib "$out/lib/libusb-1.0.0.dylib"
# pkg-config file still names the temporary prefix
sed -i.bak "s|^prefix=.*|prefix=$out|" "$out/lib/pkgconfig/libusb-1.0.pc"
rm "$out/lib/pkgconfig/libusb-1.0.pc.bak"
cp "$work/src-arm64/COPYING" "$out/COPYING"

echo "Built $(lipo -archs "$out/lib/libusb-1.0.0.dylib") libusb $VERSION into $out"
