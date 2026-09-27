package com.goldenfinger.butler

import android.content.Context
import androidx.work.CoroutineWorker
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import java.util.concurrent.TimeUnit

/**
 * KeepAliveWorker：WorkManager 周期保活（15 分钟，Doze 兼容）。
 * 只负责「确保前台服务在跑」；Go 服务自身的重启由服务内幂等处理。
 */
class KeepAliveWorker(ctx: Context, params: WorkerParameters) : CoroutineWorker(ctx, params) {
    override suspend fun doWork(): Result {
        GoForegroundService.start(applicationContext)
        return Result.success()
    }

    companion object {
        private const val UNIQUE = "keepalive"

        fun schedule(context: Context) {
            val req = PeriodicWorkRequestBuilder<KeepAliveWorker>(15, TimeUnit.MINUTES)
                .build()
            WorkManager.getInstance(context).enqueueUniquePeriodicWork(
                UNIQUE, ExistingPeriodicWorkPolicy.KEEP, req
            )
        }
    }
}
