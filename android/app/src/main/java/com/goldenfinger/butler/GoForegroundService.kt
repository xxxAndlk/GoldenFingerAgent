package com.goldenfinger.butler

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat
import androidx.lifecycle.LifecycleService
import androidx.lifecycle.lifecycleScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

/**
 * GoForegroundService：前台常驻服务。
 * 职责：启动 Go 服务（GoBridge）、维持前台通知、周期轮询 /api/outbox 转系统通知。
 * 通过 startForegroundService 启动，onStartCommand 内 5 秒内必须 startForeground。
 */
class GoForegroundService : LifecycleService() {
    private var poller: Job? = null

    override fun onCreate() {
        super.onCreate()
        startForegroundCompat()
        GoBridge.start(this)
        poller = lifecycleScope.launch { NotifyPoller(this@GoForegroundService).run() }
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        super.onStartCommand(intent, flags, startId)
        // 服务已在 onCreate 中完成初始化；重复 START_STICKY 意图只是保活信号。
        return START_STICKY
    }

    override fun onDestroy() {
        poller?.cancel()
        // 前台服务被系统杀掉时不留 Go 服务（重启会由 WorkManager/BootReceiver 拉起）。
        GoBridge.stop()
        super.onDestroy()
    }

    override fun onBind(intent: Intent): IBinder? = null

    private fun startForegroundCompat() {
        val nm = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            nm.createNotificationChannel(
                NotificationChannel(CH_FOREGROUND, "常驻服务", NotificationManager.IMPORTANCE_LOW).apply {
                    description = "管家服务运行中"
                }
            )
        }
        val pi = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
        val n: Notification = NotificationCompat.Builder(this, CH_FOREGROUND)
            .setSmallIcon(R.drawable.ic_stat_butler)
            .setContentTitle("金手指管家")
            .setContentText("管家服务运行中")
            .setContentIntent(pi)
            .setOngoing(true)
            .build()
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            startForeground(NOTIF_ID, n, android.content.pm.ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE)
        } else {
            startForeground(NOTIF_ID, n)
        }
    }

    companion object {
        const val CH_FOREGROUND = "foreground"
        const val CH_REMINDER = "reminders"
        const val NOTIF_ID = 1001

        /** 便捷启动。 */
        fun start(c: Context) {
            val i = Intent(c, GoForegroundService::class.java)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                c.startForegroundService(i)
            } else {
                c.startService(i)
            }
        }
    }
}
