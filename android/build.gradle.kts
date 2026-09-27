// 顶层构建脚本：声明 AGP 与 Kotlin 插件版本（应用模块内 apply）。
plugins {
    id("com.android.application") version "8.5.2" apply false
    id("org.jetbrains.kotlin.android") version "1.9.24" apply false
}
