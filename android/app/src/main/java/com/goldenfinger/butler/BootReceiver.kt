package com.goldenfinger.butler

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/** BootReceiver：开机自启，拉起前台服务（需 RECEIVE_BOOT_COMPLETED）。 */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        when (intent.action) {
            Intent.ACTION_BOOT_COMPLETED,
            Intent.ACTION_MY_PACKAGE_REPLACED -> {
                GoForegroundService.start(context)
            }
        }
    }
}
