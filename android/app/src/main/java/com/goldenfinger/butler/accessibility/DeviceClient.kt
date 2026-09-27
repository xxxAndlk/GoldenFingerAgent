package com.goldenfinger.butler.accessibility

import android.content.Context
import android.util.Log
import com.goldenfinger.butler.GoBridge
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import org.json.JSONObject
import java.util.concurrent.Executors
import java.util.concurrent.ScheduledExecutorService
import java.util.concurrent.TimeUnit

/**
 * DeviceClient：OkHttp WebSocket 连 ws://127.0.0.1:PORT/ws/device。
 * 协议帧逐字遵循 device-ws-contract：
 *   设备→服务：register/heartbeat/result
 *   服务→设备：registered/ping/command（收到 command 后交给 Executor，回 result）
 * 握手与所有帧走同一连接；HTTP 头带 X-Token（服务端中间件同时校验头与 query）。
 */
class DeviceClient(
    private val ctx: Context,
    private val executor: Executor,
    private val reporter: ScreenReporter
) {
    private val client = OkHttpClient.Builder()
        .pingInterval(30, TimeUnit.SECONDS)
        .build()
    private var ws: WebSocket? = null
    private val scheduler: ScheduledExecutorService = Executors.newSingleThreadScheduledExecutor()
    @Volatile private var closed = false
    @Volatile private var schedulesInstalled = false

    fun connect() {
        if (closed) return
        val addr = GoBridge.baseUrl() ?: run {
            // Go 服务尚未就绪（前台服务可能还在启动），3 秒后重试
            try { scheduler.schedule({ connect() }, 3, TimeUnit.SECONDS) } catch (_: Throwable) {}
            return
        }
        val token = GoBridge.token() ?: return
        if (!schedulesInstalled) {
            schedulesInstalled = true
            // 心跳周期 20s；断线后 5s 重连
            scheduler.scheduleAtFixedRate({ heartbeat() }, 20, 20, TimeUnit.SECONDS)
            scheduler.scheduleAtFixedRate({ if (ws == null) connect() }, 5, 5, TimeUnit.SECONDS)
        }
        val url = "ws://$addr/ws/device"
        val req = Request.Builder()
            .url(url)
            .header("X-Token", token)
            .build()
        ws = client.newWebSocket(req, listener)
    }

    fun close() {
        closed = true
        scheduler.shutdownNow()
        ws?.close(1000, "app stop")
        ws = null
    }

    private fun heartbeat() {
        if (closed) return
        val o = JSONObject()
            .put("type", "heartbeat")
            .put("device_id", reporter.deviceId())
            .put("ts", System.currentTimeMillis())
        ws?.send(o.toString())
    }

    private fun sendResult(cmdId: String, ok: Boolean, error: String = "", data: JSONObject = JSONObject()) {
        val o = JSONObject()
            .put("type", "result")
            .put("device_id", reporter.deviceId())
            .put("cmd_id", cmdId)
            .put("ok", ok)
            .put("error", error)
            .put("data", data)
        ws?.send(o.toString())
    }

    private val listener = object : WebSocketListener() {
        override fun onOpen(webSocket: WebSocket, response: Response) {
            Log.i(TAG, "WS 已连接")
            // 注册
            val o = JSONObject()
                .put("type", "register")
                .put("device_id", reporter.deviceId())
            webSocket.send(o.toString())
        }

        override fun onMessage(webSocket: WebSocket, text: String) {
            val f = try { JSONObject(text) } catch (t: Throwable) { return }
            when (f.optString("type")) {
                "registered" -> Log.d(TAG, "已注册 device_id=${f.optString("device_id")}")
                "ping" -> heartbeat()
                "command" -> {
                    val cmdId = f.optString("cmd_id")
                    val action = f.optString("action")
                    val params = f.optJSONObject("params") ?: JSONObject()
                    Log.i(TAG, "收到 command action=$action cmd_id=$cmdId")
                    try {
                        val result = executor.execute(action, params)
                        sendResult(cmdId, result.ok, result.error, result.data)
                    } catch (t: Throwable) {
                        sendResult(cmdId, false, "执行异常：${t.message}")
                    }
                    // 操作后刷新屏幕帧
                    reporter.schedule()
                }
                else -> Log.d(TAG, "未知帧 type=${f.optString("type")}")
            }
        }

        override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
            Log.w(TAG, "WS 连接失败: ${t.message}")
            ws = null
        }

        override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
            ws = null
        }
    }

    companion object {
        private const val TAG = "DeviceClient"
    }
}
