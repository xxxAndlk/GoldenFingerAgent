#!/usr/bin/env bash
# 把 web/ 的 SPA 同步进 android/gosvc/webassets，供 gomobile 内嵌。
# 手机端不再维护第二份页面；构建 AAR 前必须执行本脚本，
# 否则手机会加载上一次同步的旧页面。
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
src="$root/web"
dst="$root/android/gosvc/webassets"

if [[ ! -f "$src/index.html" ]]; then
  echo "sync-webassets: 缺少 $src/index.html" >&2
  exit 1
fi

rm -rf "$dst"
mkdir -p "$dst"
cp "$src/index.html" "$dst/index.html"
# favicon 与页面内的管家头像都指向这张应用图标，必须一起同步。
cp "$src/app-icon.png" "$dst/app-icon.png"
if [[ -d "$src/vendor" ]]; then
  cp -r "$src/vendor" "$dst/vendor"
fi

echo "sync-webassets: 已同步 $(cd "$dst" && find . -type f | wc -l) 个文件到 android/gosvc/webassets"
