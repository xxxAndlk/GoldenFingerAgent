package com.goldenfinger.butler.accessibility

import android.content.Context
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import android.util.Base64
import android.util.Log
import com.goldenfinger.butler.GoBridge
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONObject
import java.io.ByteArrayOutputStream
import java.util.concurrent.TimeUnit

/**
 * ScreenReporter：界面变化去抖 500ms 后，POST /api/screen 上报
 * {device_id, nodes, screenshot_b64}（HTTP 带 X-Token）。
 * device_id 用 ANDROID_ID 派生（稳定、无需额外权限）。
 */
class ScreenReporter(
    private val ctx: Context,
    private val dumper: NodeDumper,
    private val executor: Executor
) {
    private val handler = Handler(Looper.getMainLooper())
    private val client = OkHttpClient.Builder()
        .connectTimeout(3, TimeUnit.SECONDS)
        .readTimeout(10, TimeUnit.SECONDS)
        .build()
    private val debounce = object : Runnable {
        override fun run() { ioExecutor.execute { doReport() } }
    }
    private val ioExecutor = java.util.concurrent.Executors.newSingleThreadExecutor()

    /** 触发一次上报（去抖 500ms）。 */
    fun schedule() {
        handler.removeCallbacks(debounce)
        handler.postDelayed(debounce, 500)
    }

    private fun doReport() {
        val addr = GoBridge.baseUrl() ?: return
        val token = GoBridge.token() ?: return
        val root = ControlServiceHolder.service?.rootInActiveWindow ?: return
        val nodes = dumper.dump(root)
        val screenshot = executor.takeScreenshotB64() ?: ""

        val body = JSONObject()
            .put("device_id", deviceId())
            .put("nodes", nodes)
            .put("screenshot_b64", screenshot)
            .toString()
        val req = Request.Builder()
            .url("http://$addr/api/screen")
            .header("X-Token", token)
            .header("Content-Type", "application/json")
            .post(body.toRequestBody("application/json".toMediaType()))
            .build()
        try {
            client.newCall(req).execute().use { resp ->
                if (!resp.isSuccessful) Log.d(TAG, "screen report http ${resp.code}")
            }
        } catch (t: Throwable) {
            Log.d(TAG, "screen report failed: ${t.message}")
        }
    }

    fun deviceId(): String {
        val androidId = Settings.Secure.getString(ctx.contentResolver, Settings.Secure.ANDROID_ID) ?: "unknown"
        // 派生稳定 id：ANDROID_ID 反转后 base64（避免直接暴露系统标识）
        return Base64.encodeToString(androidId.reversed().toByteArray(), Base64.NO_WRAP)
    }

    companion object {
        private const val TAG = "ScreenReporter"
    }
}
