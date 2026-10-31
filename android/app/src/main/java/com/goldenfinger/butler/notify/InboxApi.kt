package com.goldenfinger.butler.notify

import android.util.Log
import com.goldenfinger.butler.GoBridge
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONArray
import org.json.JSONObject
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

/**
 * InboxApi：通知通道访问 Go 服务 /api/inbox/ 下各端点的唯一出口。
 * 口径照抄 NotifyPoller/GoBridge：GoBridge.baseUrl()/token()，头带 X-Token 与 X-User-Id。
 * OkHttp 同步调用跑在自带单线程后台执行器；Go 服务不可达/网络失败一律静默返回 null/false，不崩溃。
 */
object InboxApi {
    private const val TAG = "InboxApi"
    private val JSON_TYPE = "application/json; charset=utf-8".toMediaType()
    private val exec = Executors.newSingleThreadExecutor { r -> Thread(r, "inbox-api") }
    private val client = OkHttpClient.Builder()
        .connectTimeout(3, TimeUnit.SECONDS)
        .readTimeout(5, TimeUnit.SECONDS)
        .build()

    /** 后台执行；所有异常吞掉（静默跳过）。 */
    fun bg(block: () -> Unit) {
        exec.execute {
            try {
                block()
            } catch (t: Throwable) {
                Log.d(TAG, "inbox op failed: ${t.message}")
            }
        }
    }

    /** 批量上报，契约 JSON：{"events":[{source_pkg,app_name,title,text,posted_at}...]}。 */
    fun reportEvents(events: JSONArray): Boolean =
        post("/api/inbox/events", JSONObject().put("events", events).toString()) != null

    /** 单条便捷上报（服务端按 hash 去重）。 */
    fun reportEvent(sourcePkg: String, appName: String, title: String, text: String, postedAt: String): Boolean {
        val e = JSONObject()
            .put("source_pkg", sourcePkg)
            .put("app_name", appName)
            .put("title", title)
            .put("text", text)
            .put("posted_at", postedAt)
        return reportEvents(JSONArray().put(e))
    }

    /**
     * 稍后处理：只上报通知字段，由服务端匹配事件并建立延后提醒。
     * 客户端不再拉列表做 pkg+title+text 匹配（那是业务逻辑，已搬到 Go）。
     * minutes>=0 按分钟；否则用 fire_at（RFC3339，如今晚 20:00）。
     */
    fun snoozeByContent(sourcePkg: String, title: String, text: String, minutes: Long, fireAt: String?): Boolean {
        val payload = JSONObject()
            .put("source_pkg", sourcePkg)
            .put("title", title)
            .put("text", text)
        if (minutes >= 0) {
            payload.put("minutes", minutes)
        } else if (!fireAt.isNullOrBlank()) {
            payload.put("fire_at", fireAt)
        } else {
            return false
        }
        return post("/api/inbox/snooze", payload.toString()) != null
    }

    private fun post(path: String, json: String): String? {
        val addr = GoBridge.baseUrl() ?: return null
        val token = GoBridge.token() ?: return null
        val req = Request.Builder()
            .url("http://$addr$path")
            .header("X-Token", token)
            .header("X-User-Id", userHeader())
            .post(json.toRequestBody(JSON_TYPE))
            .build()
        client.newCall(req).execute().use { resp ->
            return if (resp.isSuccessful) resp.body?.string() else null
        }
    }

    private fun userHeader(): String = GoBridge.userId() ?: GoBridge.deviceId() ?: ""
}
