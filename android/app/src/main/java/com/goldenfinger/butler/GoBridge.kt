package com.goldenfinger.butler

import android.content.Context
import android.util.Log
import org.json.JSONObject

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

    private fun prefs(c: Context? = null): android.content.SharedPreferences =
        (c ?: App.instance()).getSharedPreferences(PREF, Context.MODE_PRIVATE)
}
