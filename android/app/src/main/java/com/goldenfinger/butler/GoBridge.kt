package com.goldenfinger.butler

import android.content.Context
import android.util.Log
import org.json.JSONObject
import java.io.File

/**
 * GoBridge 负责加载 gomobile 产出的 Go 服务（gosvc AAR）并管理其生命周期。
 * StartServer 返回的 info JSON 含 {addr, token, device_id}，存入 SharedPreferences
 * 供 WebView / 无障碍通道 / 通知轮询共享。
 */
object GoBridge {
    private const val TAG = "GoBridge"
    private const val PREF = "gosvc"
    private const val KEY_ADDR = "addr"
    private const val KEY_TOKEN = "token"
    private const val KEY_DEVICE_ID = "device_id"
    private const val KEY_USER_ID = "user_id"

    @Volatile
    private var started = false

    fun isRunning(): Boolean = started

    /** 启动 Go 服务。dataDir 为 App 私有 files 目录。 */
    @Synchronized
    fun start(context: Context) {
        if (started) return
        try {
            val dataDir = context.filesDir.absolutePath
            installRelayCA(context, dataDir)
            // 必须在 Go 读取云端 Token 前注册；Android 不允许明文文件回退。
            gosvc.Gosvc.setSecretStore(AndroidSecretStore())
            val info = gosvc.Gosvc.startServer(dataDir)
            if (info.isNullOrEmpty()) {
                Log.e(TAG, "startServer 返回空 info")
                return
            }
            val j = JSONObject(info)
            prefs(context).edit()
                .putString(KEY_ADDR, j.optString("addr"))
                .putString(KEY_TOKEN, j.optString("token"))
                .putString(KEY_DEVICE_ID, j.optString("device_id"))
                .apply()
            started = true
            Log.i(TAG, "Go 服务已启动 addr=${j.optString("addr")}")
        } catch (t: Throwable) {
            Log.e(TAG, "启动 Go 服务失败", t)
        }
    }

    @Synchronized
    fun stop() {
        if (!started) return
        try {
            gosvc.Gosvc.stopServer()
        } catch (t: Throwable) {
            Log.e(TAG, "停止 Go 服务失败", t)
        }
        started = false
    }

    fun baseUrl(): String? = prefs().getString(KEY_ADDR, null)
    fun token(): String? = prefs().getString(KEY_TOKEN, null)
    fun deviceId(): String? = prefs().getString(KEY_DEVICE_ID, null)
    fun userId(): String? = prefs().getString(KEY_USER_ID, null)
    fun saveUserId(id: String) = prefs().edit().putString(KEY_USER_ID, id).apply()

    /**
     * 把内置的中继 CA 证书复制到 App 私有目录。
     *
     * 中继没有域名、只能用自签证书，手机必须额外信任这张 CA 才连得上；
     * 否则只能跳过证书校验，那等于把配对码和指令暴露给中间人。
     * 已存在时不覆盖：用户可能自己换过证书，静默覆盖会让配对失败却查不到原因。
     */
    private fun installRelayCA(context: Context, dataDir: String) {
        val target = File(dataDir, "relay-ca.pem")
        if (target.exists()) return
        try {
            context.assets.open("relay-ca.pem").use { input ->
                target.outputStream().use { output -> input.copyTo(output) }
            }
            target.setReadable(false, false)
            target.setReadable(true, true)
            Log.i(TAG, "已安装中继 CA 证书 ${target.absolutePath}")
        } catch (t: Throwable) {
            // 没内置证书是正常情况（例如以后改用正式域名 + 系统信任库）。
            Log.i(TAG, "未内置中继 CA 证书，跳过")
        }
    }

    private fun prefs(c: Context? = null): android.content.SharedPreferences =
        (c ?: App.instance).getSharedPreferences(PREF, Context.MODE_PRIVATE)
}
