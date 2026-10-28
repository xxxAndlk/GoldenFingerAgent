package com.goldenfinger.butler.memory

import android.util.Log
import com.goldenfinger.butler.GoBridge
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONObject
import java.time.Instant
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

/** 将最终转写片段发送到本机 Go 服务；不会发送音频。 */
object MemoryCaptureApi {
    private const val TAG = "MemoryCaptureApi"
    private val jsonType = "application/json; charset=utf-8".toMediaType()
    private val executor = Executors.newSingleThreadExecutor { r -> Thread(r, "memory-capture-api") }
    private val client = OkHttpClient.Builder()
        .connectTimeout(3, TimeUnit.SECONDS)
        .readTimeout(60, TimeUnit.SECONDS)
        .build()
    @Volatile
    private var sessionId: String = ""

    fun chat(text: String, onReply: (String) -> Unit) {
        executor.execute {
            try {
                val addr = GoBridge.baseUrl() ?: throw IllegalStateException("Go service unavailable")
                val token = GoBridge.token() ?: throw IllegalStateException("token unavailable")
                val payload = JSONObject().put("text", text)
                if (sessionId.isNotBlank()) payload.put("session_id", sessionId)
                val request = Request.Builder()
                    .url("http://$addr/api/chat")
                    .header("X-Token", token)
                    .header("X-User-Id", GoBridge.userId() ?: GoBridge.deviceId() ?: "")
                    .post(payload.toString().toRequestBody(jsonType))
                    .build()
                client.newCall(request).execute().use { response ->
                    if (!response.isSuccessful) throw IllegalStateException("chat HTTP ${response.code}")
                    val body = JSONObject(response.body?.string().orEmpty())
                    sessionId = body.optString("session_id", sessionId)
                    val reply = body.optString("reply").ifBlank { "我没有听清，请再说一次。" }
                    onReply(reply)
                }
            } catch (t: Throwable) {
                Log.w(TAG, "chat failed: ${t.message}")
                onReply("我刚才没连上管家，请再试一次。")
            }
        }
    }

    fun capture(text: String, contextBefore: String, contextAfter: String, capturedAtMillis: Long) {
        executor.execute {
            try {
                post(text, contextBefore, contextAfter, capturedAtMillis)
            } catch (t: Throwable) {
                // 网络暂时不可达时丢弃本次上报，避免在后台无限积累私人语音文本。
                Log.w(TAG, "capture failed: ${t.message}")
            }
        }
    }

    private fun post(text: String, before: String, after: String, atMillis: Long) {
        val addr = GoBridge.baseUrl() ?: return
        val token = GoBridge.token() ?: return
        val payload = JSONObject()
            .put("text", text)
            .put("context_before", before)
            .put("context_after", after)
            .put("source", "android_speech")
            .put("captured_at", Instant.ofEpochMilli(atMillis).toString())
            .toString()
        val request = Request.Builder()
            .url("http://$addr/api/memory/capture")
            .header("X-Token", token)
            .header("X-User-Id", GoBridge.userId() ?: GoBridge.deviceId() ?: "")
            .post(payload.toRequestBody(jsonType))
            .build()
        client.newCall(request).execute().use { response ->
            if (!response.isSuccessful) {
                Log.w(TAG, "capture HTTP ${response.code}")
            }
        }
    }
}
