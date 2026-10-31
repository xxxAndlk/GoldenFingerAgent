@echo off
chcp 65001 >nul
cd /d "%~dp0"
title 启动金手指管家
set "APP=%~dp0desktop\dist\GoldenFinger-win\GoldenFinger.exe"
if not exist "%APP%" (
  echo 找不到完整桌面版：%APP%
  echo 请先构建并打包桌面版。
  pause
  exit /b 1
)
rem 清除开发环境变量，避免 Electron 被当成 Node.js 启动。
set "ELECTRON_RUN_AS_NODE="
start "" "%APP%"
exit /b 0
