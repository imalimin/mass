#!/bin/sh
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
ANDROID_DIR="$ROOT/proj/android"
JNILIBS_DIR="$ANDROID_DIR/app/src/main/jniLibs"
API_LEVEL="${ANDROID_API_LEVEL:-24}"
ABIS="${ABIS:-arm64-v8a}"
BUILD_APK=0
BUILD_FRONTEND=0

for arg in "$@"; do
  case "$arg" in
    --apk) BUILD_APK=1 ;;
    --with-frontend) BUILD_FRONTEND=1 ;;
    --all-abis) ABIS="arm64-v8a x86_64" ;;
    *)
      echo "Usage: $0 [--with-frontend] [--all-abis] [--apk]" >&2
      exit 1
      ;;
  esac
done

if [ "$BUILD_FRONTEND" -eq 1 ] || [ ! -f "$ROOT/internal/transport/http/ui/dist/index.html" ]; then
  echo "==> Building embedded frontend..."
  (cd "$ROOT" && npm --prefix frontend run build && node scripts/embed.mjs)
fi

detect_sdk() {
  if [ -n "${ANDROID_HOME:-}" ] && [ -d "$ANDROID_HOME" ]; then
    echo "$ANDROID_HOME"
    return
  fi
  if [ -n "${ANDROID_SDK_ROOT:-}" ] && [ -d "$ANDROID_SDK_ROOT" ]; then
    echo "$ANDROID_SDK_ROOT"
    return
  fi
  if [ -f "$ANDROID_DIR/local.properties" ]; then
    sdk=$(sed -n 's/^sdk\.dir=//p' "$ANDROID_DIR/local.properties" | head -n 1 | tr -d '\r')
    if [ -n "$sdk" ] && [ -d "$sdk" ]; then
      echo "$sdk"
      return
    fi
  fi
  if [ -d "$HOME/Library/Android/sdk" ]; then
    echo "$HOME/Library/Android/sdk"
    return
  fi
  return 1
}

detect_ndk() {
  if [ -n "${ANDROID_NDK_HOME:-}" ] && [ -d "$ANDROID_NDK_HOME" ]; then
    echo "$ANDROID_NDK_HOME"
    return
  fi
  if [ -n "${ANDROID_NDK_ROOT:-}" ] && [ -d "$ANDROID_NDK_ROOT" ]; then
    echo "$ANDROID_NDK_ROOT"
    return
  fi
  sdk=$(detect_sdk || true)
  if [ -n "$sdk" ] && [ -d "$sdk/ndk" ]; then
    latest=$(ls -1 "$sdk/ndk" | sort -V | tail -n 1)
    if [ -n "$latest" ] && [ -d "$sdk/ndk/$latest" ]; then
      echo "$sdk/ndk/$latest"
      return
    fi
  fi
  echo "Android NDK not found. Set ANDROID_NDK_HOME or install NDK in Android SDK." >&2
  exit 1
}

NDK_DIR=$(detect_ndk)
PREBUILT_DIR=""
for candidate in "$NDK_DIR/toolchains/llvm/prebuilt/darwin-arm64" \
                 "$NDK_DIR/toolchains/llvm/prebuilt/darwin-x86_64" \
                 "$NDK_DIR/toolchains/llvm/prebuilt/linux-x86_64"; do
  if [ -d "$candidate/bin" ]; then
    PREBUILT_DIR="$candidate"
    break
  fi
done

if [ -z "$PREBUILT_DIR" ]; then
  echo "Could not locate LLVM toolchain under $NDK_DIR" >&2
  exit 1
fi

VERSION=$(git -C "$ROOT" describe --always --dirty 2>/dev/null || echo "android-dev")
echo "==> Using NDK: $NDK_DIR"
echo "==> Version: $VERSION"

for abi in $ABIS; do
  case "$abi" in
    arm64-v8a)
      GOARCH=arm64
      CLANG="$PREBUILT_DIR/bin/aarch64-linux-android${API_LEVEL}-clang"
      ;;
    x86_64)
      GOARCH=amd64
      CLANG="$PREBUILT_DIR/bin/x86_64-linux-android${API_LEVEL}-clang"
      ;;
    armeabi-v7a)
      GOARCH=arm
      CLANG="$PREBUILT_DIR/bin/armv7a-linux-androideabi${API_LEVEL}-clang"
      ;;
    *)
      echo "Unsupported ABI: $abi" >&2
      exit 1
      ;;
  esac

  if [ ! -x "$CLANG" ]; then
    echo "Compiler not found or not executable: $CLANG" >&2
    exit 1
  fi

  OUT_DIR="$JNILIBS_DIR/$abi"
  mkdir -p "$OUT_DIR"
  OUT_SO="$OUT_DIR/libmassbridge.so"
  echo "==> Building $OUT_SO (GOOS=android GOARCH=$GOARCH)..."
  (
    cd "$ROOT"
    CGO_ENABLED=1 \
    GOOS=android \
    GOARCH="$GOARCH" \
    CC="$CLANG" \
    CGO_LDFLAGS="-Wl,-soname,libmassbridge.so" \
    sh scripts/go.sh build \
      -mod=readonly \
      -buildmode=c-shared \
      -buildvcs=false \
      -trimpath \
      -ldflags="-s -w -X main.version=$VERSION" \
      -o "$OUT_SO" \
      ./cmd/mass-android
  )
  rm -f "$OUT_DIR/libmassbridge.h"
  ls -lh "$OUT_SO"
done

if [ "$BUILD_APK" -eq 1 ]; then
  echo "==> Building Android Debug APK..."
  (cd "$ANDROID_DIR" && ./gradlew assembleDebug)
fi
