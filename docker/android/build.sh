#!/usr/bin/env bash
# 在 gfa-android-build 镜像内构建 Android AAR + APK。
#
# 用法（仓库根目录）：
#   docker run --rm -v "$PWD:/work" gfa-android-build:latest \
#     bash /work/docker/android/build.sh
#
# 产物（写回宿主仓库）：
#   android/app/libs/gosvc.aar
#   android/app/build/outputs/apk/debug/app-debug.apk
#
# 说明：本脚本必须在带 JDK17 + Android SDK/NDK + gomobile 的镜像里运行；
# 宿主 Windows 不需要安装 Android 工具链。
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$root"

echo "==> 1/4 同步 web 页面与中继 CA 到 android 资源"
bash "$root/scripts/sync-webassets.sh"
# 中继用自签证书时，手机必须额外信任这张 CA（assets/relay-ca.pem）。
# 证书来自 server-dist/relay-tls/，没有就跳过（走正式域名时不需要）。
if [[ -f "$root/server-dist/relay-tls/relay-ca.pem" ]]; then
  mkdir -p android/app/src/main/assets
  cp "$root/server-dist/relay-tls/relay-ca.pem" android/app/src/main/assets/relay-ca.pem
  echo "    已同步中继 CA 证书到 assets/"
else
  echo "    未找到 server-dist/relay-tls/relay-ca.pem，跳过（走正式域名时不需要）"
fi

echo "==> 2/4 gomobile bind：Go 服务 → gosvc.aar"
mkdir -p android/app/libs
# 只打手机实际需要的 ABI（arm64 + armv7），把 AAR/APK 体积砍掉近一半；
# 需要模拟器时用 GFA_ANDROID_TARGETS=android/amd64 覆盖。
targets="${GFA_ANDROID_TARGETS:-android/arm64,android/arm}"
# gomobile 首次运行需要 gobind（Dockerfile 已装 gomobile，但 gobind 由 init 生成）。
gomobile init
# -s -w 去掉符号表/调试信息：体积可降 20%~30%，代价是无法用 dlv 调试 Go 侧。
gomobile bind -target="$targets" -androidapi 26   -ldflags="-s -w" -trimpath   -o android/app/libs/gosvc.aar ./android/gosvc

echo "==> 3/4 gradle assembleDebug"
cd "$root/android"
if [[ -x ./gradlew ]]; then
  ./gradlew --no-daemon :app:assembleDebug
else
  gradle --no-daemon :app:assembleDebug
fi

echo "==> 4/4 校验产物"
apk="app/build/outputs/apk/debug/app-debug.apk"
test -f "$apk" || { echo "构建未产出 APK" >&2; exit 1; }
ls -lh "$apk" ../android/app/libs/gosvc.aar
echo "ANDROID_BUILD_OK apk=$root/android/$apk"
