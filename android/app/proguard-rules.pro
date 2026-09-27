# 金手指管家混淆规则
# gomobile AAR：保留 Go 导出类
-keep class go.** { *; }
-keep class gosvc.** { *; }
-keep class com.goldenfinger.butler.gosvc.** { *; }

# OkHttp
-dontwarn okhttp3.**
-dontwarn okio.**

# 保留无障碍服务/前台服务类名（Manifest 引用）
-keep class com.goldenfinger.butler.accessibility.** { *; }
-keep class com.goldenfinger.butler.GoForegroundService { *; }
-keep class com.goldenfinger.butler.BootReceiver { *; }
