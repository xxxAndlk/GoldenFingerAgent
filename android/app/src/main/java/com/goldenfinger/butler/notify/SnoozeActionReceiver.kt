package com.goldenfinger.butler.notify

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.util.Log
import java.time.Instant
import java.time.LocalDate
import java.time.ZoneId
import java.time.format.DateTimeFormatter

/**
 * SnoozeActionReceiver：转述通知上「稍后处理」action 的入口。
 * minutes>=0 直接按分钟；minutes<0 用 fire_at=今晚20:00（已过则次日20:00，点击时计算）。
 * event_id 需回查 /api/inbox/events 取（上报响应不含 id）；找不到或网络失败一律静默放弃。
 */
class SnoozeActionReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val pkg = intent.getStringExtra(EXTRA_SOURCE_PKG) ?: return
        val title = intent.getStringExtra(EXTRA_TITLE).orEmpty()
        val text = intent.getStringExtra(EXTRA_TEXT).orEmpty()
        val minutes = intent.getLongExtra(EXTRA_MINUTES, -1L)
        val fireAt = if (minutes < 0) tonightFireAt() else null
        val pending = goAsync()
        InboxApi.bg {
            try {
                InboxApi.snoozeByContent(pkg, title, text, minutes, fireAt)
            } catch (t: Throwable) {
                Log.d(TAG, "snooze failed: ${t.message}")
            } finally {
                pending.finish()
            }
        }
    }

    companion object {
        private const val TAG = "SnoozeActionReceiver"
        const val EXTRA_SOURCE_PKG = "source_pkg"
        const val EXTRA_TITLE = "title"
        const val EXTRA_TEXT = "text"
        const val EXTRA_MINUTES = "minutes"

        /** 今晚 = 今天 20:00（已过则次日 20:00），RFC3339。 */
        fun tonightFireAt(): String {
            val zone = ZoneId.systemDefault()
            var t = LocalDate.now(zone).atTime(20, 0).atZone(zone)
            if (!t.toInstant().isAfter(Instant.now())) t = t.plusDays(1)
            return t.format(DateTimeFormatter.ISO_OFFSET_DATE_TIME)
        }
    }
}
