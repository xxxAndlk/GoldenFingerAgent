# 金手指管家 · Android 工程

本目录是「金手指管家」的 Android 客户端：Go 服务（gomobile bind 封装）作为
**本机 127.0.0.1 随机端口**的 HTTP/WS 服务，Kotlin App 壳以 WebView 加载其页面，
并内置**无障碍服务**（查看屏幕 / 执行操作 / 截图）把手机变成可操控设备，由
后台服务轮询 outbox 下发提醒通知。

> **重要声明：本机无 Android SDK / JDK / NDK，本工程未在本机实编。**
> 以下构建步骤需在**已安装 Android Studio 的机器**上执行；Go 侧代码（`gosvc/`）
> 不依赖 Android 工具链，`go build ./android/gosvc` 可在仓库根直接编译验证。

## 一、环境准备

- Android Studio **Koala 2024.1.1+**（内置 JDK 17）
- Android SDK **Platform 34**（Android Studio 首次启动自动下载）
- **不需要** NDK（Go 服务用 gomobile 预编译为 AAR，Kotlin 壳不写原生 C/C++）
- Go 1.22+（构建 gosvc AAR 用，见步骤二）

## 二、构建步骤

在**仓库根目录**（`D:\WebData\github\GoldenFingerAgent`）执行：

```bash
# 1) 安装 gomobile 工具链（一次即可）
go install golang.org/x/mobile/cmd/gomobile@latest
go get golang.org/x/mobile/bind
# （在 golang.org/x/mobile 模块中）首次需要
cd $(go env GOPATH)/pkg/mod/golang.org/x/mobile@*/ 2>/dev/null || true

# 2) 编译 Go 服务为 Android AAR，放入 app/libs/
mkdir -p android/app/libs
cd android
# gomobile 需要 go.mod 位于 android/（或仓库根），这里推荐在仓库根执行：
cd ..
gomobile bind -target=android -o android/app/libs/gosvc.aar ./android/gosvc

# 3) 用 Android Studio 打开 android/ 目录，或命令行：
cd android
./gradlew :app:assembleDebug
```

产物：`android/app/build/outputs/apk/debug/app-debug.apk`

> `gomobile bind` 会生成 `gosvc.aar`（内含 Go 服务与导出类 `gosvc.Gosvc`，
> 由 `app/libs/` 下的 `fileTree("libs")` 依赖引入）。

## 三、安装与首次引导

1. 安装 APK，打开「金手指管家」。
2. 首次进入自动弹出**权限引导页**（`PermissionsActivity`），按顺序开启：
   - **无障碍服务**（查看/操控屏幕的通道）
   - **悬浮窗**（操作悬浮条与停止按钮）
   - **电池优化白名单**（防止服务被杀）
   - **通知**（到点提醒）
3. 无障碍开启后，管家即可「看」到屏幕：对管家说「帮我把某某打开/点一下」，
   后台通过 WebSocket（`ws://127.0.0.1:PORT/ws/device`）下发指令并执行。
4. 按**音量键**连续 3 次或长按 1 秒，可随时**急停**（10 秒内不再自动操作）。

## 数据与安全

- Go 服务的 config / settings / SQLite（`gfa.db`）/ token / device.id 全部落在
  **App 私有目录**（`filesDir`）。
- 服务只监听 **127.0.0.1 随机端口**，所有请求（含 `/ws/device` 握手）都校验
  **随机 token**（`X-Token` 头或 URL query `token`），外部无法访问。
- 付款/转账/删除/下单类敏感操作会弹**人工确认**，未确认不执行。

## 目录结构

```
android/
  settings.gradle.kts / build.gradle.kts / gradle.properties / local.properties.example
  gosvc/gosvc.go            # gomobile bind 包：StartServer/StopServer
  gosvc/webassets/          # 内嵌精简 Web 首页（首次启动写入 dataDir/web/）
  app/
    build.gradle.kts        # minSdk26 targetSdk34 compileSdk34
    src/main/AndroidManifest.xml
    src/main/java/com/goldenfinger/butler/
      MainActivity.kt / GoBridge.kt / GoForegroundService.kt / BootReceiver.kt
      KeepAliveWorker.kt / NotifyPoller.kt / PermissionsActivity.kt / App.kt
      accessibility/        # ControlService/NodeDumper/ScreenReporter/DeviceClient/
                            # Executor/SafetyGuard/OverlayManager/KeyEmergency
    src/main/res/           # 图标（自适应矢量）/启动页/strings/colors/themes/layout/xml
```
