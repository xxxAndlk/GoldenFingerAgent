package com.goldenfinger.butler

import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.os.Build
import android.util.Log
import androidx.core.app.NotificationCompat
import kotlinx.coroutines.delay
import org.json.JSONArray
import okhttp3.OkHttpClient
import okhttp3.Request
import java.util.concurrent.TimeUnit

/**
 * NotifyPoller：服务内周期轮询 GET /api/outbox（带 X-Token），
 * 把新提醒转成系统通知（高重要性 channel，可响铃震动）。
 * 返回后 outbox 被 Drain，天然去重。
 */
class NotifyPoller(private val ctx: Context) {
    private val client = OkHttpClient.Builder()
        .connectTimeout(3, TimeUnit.SECONDS)
        .readTimeout(5, TimeUnit.SECONDS)
        .build()

    /** 常驻轮询：每 10 秒查一次；Go 服务未启动时静默跳过。 */
    suspend fun run() {
        ensureChannel()
        while (true) {
            try {
                pollOnce()
            } catch (t: Throwable) {
                Log.d(TAG, "poll failed: ${t.message}")
            }
            delay(10_000)
        }
    }

    private fun pollOnce() {
        val addr = GoBridge.baseUrl() ?: return
        val token = GoBridge.token() ?: return
        val req = Request.Builder()
            .url("http://$addr/api/outbox")
            .header("X-Token", token)
            .get()
            .build()
        client.newCall(req).execute().use { resp ->
            if (!resp.isSuccessful) return
            val body = resp.body?.string() ?: return
            val arr = JSONArray(body)
            for (i in 0 until arr.length()) {
                val item = arr.getJSONObject(i)
                notify(item.optString("body", "你有新的提醒"), item.optString("at", ""))
            }
        }
    }

    private fun notify(text: String, at: String) {
        val pi = PendingIntent.getActivity(
            ctx, 0, Intent(ctx, MainActivity::class.java),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
        val n = NotificationCompat.Builder(ctx, GoForegroundService.CH_REMINDER)
            .setSmallIcon(R.drawable.ic_stat_butler)
            .setContentTitle("金手指管家提醒")
            .setContentText(if (at.isBlank()) text else "$text（$at）")
            .setStyle(NotificationCompat.BigTextStyle().bigText(text))
            .setContentIntent(pi)
            .setAutoCancel(true)
            .setPriority(NotificationCompat.PRIORITY_HIGH)
            .build()
        val nm = ctx.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        nm.notify((System.currentTimeMillis() % 100000).toInt(), n)
    }

    private fun ensureChannel() {
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            val nm = ctx.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
            nm.createNotificationChannel(
                NotificationChannel(
                    GoForegroundService.CH_REMINDER, "管家提醒",
                    NotificationManager.IMPORTANCE_HIGH
                ).apply {
                    description = "到点提醒与通知"
                    enableVibration(true)
                }
            )
        }
    }

    companion object {
        private const val TAG = "NotifyPoller"
    }
}
