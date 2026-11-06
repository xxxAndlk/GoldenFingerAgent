package com.goldenfinger.butler.notify

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Intent
import android.os.Handler
import android.os.Looper
import android.service.notification.NotificationListenerService
import android.service.notification.StatusBarNotification
import android.util.Log
import androidx.core.app.NotificationCompat
import com.goldenfinger.butler.MainActivity
import com.goldenfinger.butler.R
import org.json.JSONArray
import org.json.JSONObject
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter

/**
 * NotifyListenerService：旁路采集授权 App 的通知 → 批量上报 Go /api/inbox/events。
 * 社交白名单 App 的消息额外发「通知助手」channel 转述通知（含 10分钟/1小时/今晚 稍后处理 action）。
 * 绝不 cancel/改写原通知；Go 服务不可达/网络失败静默跳过。
 */
class NotifyListenerService : NotificationListenerService() {
    private val buffer = ArrayList<JSONObject>()
    private val handler = Handler(Looper.getMainLooper())
    private val flushRunnable = Runnable { flush() }
    private val lastPosted = HashMap<String, Long>()

    override fun onCreate() {
        super.onCreate()
        val nm = getSystemService(NOTIFICATION_SERVICE) as NotificationManager
        nm.createNotificationChannel(
            NotificationChannel(
                CHANNEL_RELAY,
                getString(R.string.relay_channel_name),
                NotificationManager.IMPORTANCE_DEFAULT
            ).apply { description = getString(R.string.relay_channel_desc) }
        )
    }

    override fun onNotificationPosted(sbn: StatusBarNotification) {
        try {
            handle(sbn)
        } catch (t: Throwable) {
            Log.d(TAG, "handle failed: ${t.message}")
        }
    }

    private fun handle(sbn: StatusBarNotification) {
        val pkg = sbn.packageName ?: return
        if (pkg == packageName) return                                   // 防回环：自身包名不采集
        val n = sbn.notification ?: return
        if (n.flags and Notification.FLAG_GROUP_SUMMARY != 0) return     // 合并组摘要，等子条目
        if (sbn.isOngoing) return                                        // 常驻/进度类不是消息
        val extras = n.extras
        val title = extras.getCharSequence(Notification.EXTRA_TITLE)?.toString()?.trim().orEmpty()
        val text = extras.getCharSequence(Notification.EXTRA_TEXT)?.toString()?.trim().orEmpty()
        if (title.isEmpty() && text.isEmpty()) return

        val key = "$pkg|$title|$text"
        val now = System.currentTimeMillis()
        if (now - (lastPosted[key] ?: 0L) < DEBOUNCE_MS) return          // 60s 内同 pkg+title+text 去抖
        if (lastPosted.size > MAX_DEDUP_ENTRIES) lastPosted.clear()
        lastPosted[key] = now

        val appName = appLabel(pkg) ?: pkg
        val postedAt = Instant.ofEpochMilli(sbn.postTime)
            .atZone(ZoneId.systemDefault())
            .format(DateTimeFormatter.ISO_OFFSET_DATE_TIME)

        buffer.add(
            JSONObject()
                .put("source_pkg", pkg)
                .put("app_name", appName)
                .put("title", title)
                .put("text", text)
                .put("posted_at", postedAt)
        )
        handler.removeCallbacks(flushRunnable)
        if (buffer.size >= BATCH_MAX) {
            flush()
        } else {
            handler.postDelayed(flushRunnable, FLUSH_DELAY_MS)
        }

        if (isSocial(pkg)) relay(appName, pkg, title, text, sbn.postTime)
    }

    private fun flush() {
        if (buffer.isEmpty()) return
        val arr = JSONArray()
        for (e in buffer) arr.put(e)
        buffer.clear()
        InboxApi.bg { InboxApi.reportEvents(arr) }
    }

    /** 转述通知：新增独立通知（不改原通知），标题“{app}来消息”，BigText 原文 + 三个稍后处理 action。 */
    private fun relay(appName: String, pkg: String, title: String, text: String, whenMs: Long) {
        val full = if (title.isBlank() || title == text) text else "$title：$text"
        // 转述通知点开的是收件箱：用户看到的是“某 App 来消息”，
        // 打开收件箱比打开首页更接近他想找的东西。
        val openIntent = Intent(this, MainActivity::class.java)
            .putExtra(MainActivity.EXTRA_ROUTE, "inbox")
        val pi = PendingIntent.getActivity(
            this, 0, openIntent,
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
        val builder = NotificationCompat.Builder(this, CHANNEL_RELAY)
            .setSmallIcon(R.drawable.ic_stat_butler)
            .setContentTitle(getString(R.string.relay_body_fmt, appName))
            .setContentText(full)
            .setStyle(NotificationCompat.BigTextStyle().bigText(full))
            .setContentIntent(pi)
            .setAutoCancel(true)
            .setWhen(whenMs)
        addSnoozeAction(builder, pkg, title, text, 1, 10_000L)
        addSnoozeAction(builder, pkg, title, text, 2, 3_600_000L)
        addSnoozeAction(builder, pkg, title, text, 3, -1L)               // 今晚=点击时按 20:00 计算
        val nm = getSystemService(NOTIFICATION_SERVICE) as NotificationManager
        nm.notify("$pkg|$title|$text".hashCode(), builder.build())
    }

    private fun addSnoozeAction(
        builder: NotificationCompat.Builder,
        pkg: String, title: String, text: String,
        seq: Int, minutes: Long
    ) {
        val label = when (seq) {
            1 -> getString(R.string.relay_action_10min)
            2 -> getString(R.string.relay_action_1h)
            else -> getString(R.string.relay_action_tonight)
        }
        val intent = Intent(this, SnoozeActionReceiver::class.java)
            .putExtra(SnoozeActionReceiver.EXTRA_SOURCE_PKG, pkg)
            .putExtra(SnoozeActionReceiver.EXTRA_TITLE, title)
            .putExtra(SnoozeActionReceiver.EXTRA_TEXT, text)
            .putExtra(SnoozeActionReceiver.EXTRA_MINUTES, minutes)
        val pi = PendingIntent.getBroadcast(
            this, "$pkg|$title|$text".hashCode() * 4 + seq,
            intent, PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
        builder.addAction(0, label, pi)
    }

    /** 社交白名单：包名含任意规则子串即命中；SharedPreferences 可覆盖默认。 */
    private fun isSocial(pkg: String): Boolean {
        val custom = getSharedPreferences(PREF_RELAY, MODE_PRIVATE).getString(KEY_WHITELIST, null)
        val rules = if (custom.isNullOrBlank()) DEFAULT_SOCIAL
        else custom.split(',').map { it.trim() }.filter { it.isNotEmpty() }
        return rules.any { pkg.contains(it) }
    }

    private fun appLabel(pkg: String): String? = try {
        packageManager.getApplicationLabel(packageManager.getApplicationInfo(pkg, 0)).toString()
    } catch (t: Throwable) {
        null
    }

    companion object {
        private const val TAG = "NotifyListener"
        private const val PREF_RELAY = "notify_relay"
        private const val KEY_WHITELIST = "social_whitelist"
        private const val DEBOUNCE_MS = 60_000L
        private const val BATCH_MAX = 10
        private const val FLUSH_DELAY_MS = 2_000L
        private const val MAX_DEDUP_ENTRIES = 256
        const val CHANNEL_RELAY = "relay_assistant"

        // 契约默认白名单：包名含 com.tencent.mm/mm/sms/messaging 视为社交消息
        private val DEFAULT_SOCIAL = listOf("com.tencent.mm", "mm", "sms", "messaging")
    }
}
