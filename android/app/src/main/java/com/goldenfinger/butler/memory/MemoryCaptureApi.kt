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
    /** 口令判定动作：sleep/speak/chat/capture（capture 已由 Go 落库，此处无需处理）。 */
    data class Action(val type: String, val text: String)

    /** Go 的判定结果：要执行的动作 + 无后文时该等多久再 flush。 */
    data class Verdict(val actions: List<Action>, val nextFlushMs: Long)

    private const val TAG = "MemoryCaptureApi"
    private const val DEFAULT_MERGE_MS = 4000L
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

    /**
     * 把一条最终转写交给 Go 判定（唤醒词/指令窗/片段合并都在 Go），
     * 回调里带回要执行的动作。片段落库也由 Go 完成，这里不再单独上报。
     */
    fun onText(deviceId: String, text: String, flush: Boolean, onVerdict: (Verdict) -> Unit) {
        executor.execute {
            try {
                onVerdict(postVoice(deviceId, text, flush))
            } catch (t: Throwable) {
                Log.w(TAG, "voice verdict failed: ${t.message}")
                onVerdict(Verdict(emptyList(), DEFAULT_MERGE_MS))
            }
        }
    }

    private fun postVoice(deviceId: String, text: String, flush: Boolean): Verdict {
        val addr = GoBridge.baseUrl() ?: throw IllegalStateException("Go service unavailable")
        val token = GoBridge.token() ?: throw IllegalStateException("token unavailable")
        val payload = JSONObject()
            .put("device_id", deviceId)
            .put("text", text)
            .put("flush", flush)
        val request = Request.Builder()
            .url("http://$addr/api/voice/text")
            .header("X-Token", token)
            .header("X-User-Id", GoBridge.userId() ?: GoBridge.deviceId() ?: "")
            .post(payload.toString().toRequestBody(jsonType))
            .build()
        client.newCall(request).execute().use { response ->
            if (!response.isSuccessful) throw IllegalStateException("voice HTTP ${response.code}")
            val body = JSONObject(response.body?.string().orEmpty())
            val arr = body.optJSONArray("actions") ?: org.json.JSONArray()
            val actions = ArrayList<Action>(arr.length())
            for (i in 0 until arr.length()) {
                val item = arr.optJSONObject(i) ?: continue
                actions.add(Action(item.optString("type"), item.optString("text")))
            }
            val nextFlush = body.optLong("next_flush_ms", DEFAULT_MERGE_MS)
            return Verdict(actions, if (nextFlush > 0) nextFlush else DEFAULT_MERGE_MS)
        }
    }
}
