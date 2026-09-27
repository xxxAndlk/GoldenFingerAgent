package com.goldenfinger.butler

import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.PowerManager
import android.provider.Settings
import android.view.accessibility.AccessibilityManager
import android.widget.Button
import android.widget.TextView
import androidx.appcompat.app.AppCompatActivity
import com.goldenfinger.butler.accessibility.ControlService

/**
 * PermissionsActivity：无障碍 / 悬浮窗 / 电池优化白名单 / 通知 四项权限引导。
 * 每项显示图文说明与「去开启」按钮（跳对应系统设置页），返回后自动刷新状态。
 */
class PermissionsActivity : AppCompatActivity() {
    private lateinit var a11yStatus: TextView
    private lateinit var overlayStatus: TextView
    private lateinit var batteryStatus: TextView
    private lateinit var notifStatus: TextView

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_permissions)

        a11yStatus = findViewById(R.id.a11y_status)
        overlayStatus = findViewById(R.id.overlay_status)
        batteryStatus = findViewById(R.id.battery_status)
        notifStatus = findViewById(R.id.notif_status)

        findViewById<Button>(R.id.btn_a11y).setOnClickListener {
            startActivity(Intent(Settings.ACTION_ACCESSIBILITY_SETTINGS))
        }
        findViewById<Button>(R.id.btn_overlay).setOnClickListener {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
                startActivity(Intent(Settings.ACTION_MANAGE_OVERLAY_PERMISSION,
                    Uri.parse("package:$packageName")))
            }
        }
        findViewById<Button>(R.id.btn_battery).setOnClickListener {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M) {
                startActivity(Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS,
                    Uri.parse("package:$packageName")))
            }
        }
        findViewById<Button>(R.id.btn_notif).setOnClickListener {
            startActivity(Intent(Settings.ACTION_APP_NOTIFICATION_SETTINGS)
                .putExtra(Settings.EXTRA_APP_PACKAGE, packageName))
        }
    }

    override fun onResume() {
        super.onResume()
        refresh()
    }

    private fun refresh() {
        a11yStatus.text = if (isAccessibilityEnabled()) "✅ 已开启" else "⚠️ 未开启（需要它才能操控手机）"
        overlayStatus.text = if (Settings.canDrawOverlays(this)) "✅ 已授权" else "⚠️ 未授权（需要它显示操作悬浮条）"
        batteryStatus.text = if (isIgnoringBatteryOptimizations()) "✅ 已加入白名单" else "⚠️ 未加入（建议加入以免服务被省电杀掉）"
        notifStatus.text = if (areNotificationsEnabled()) "✅ 已开启" else "⚠️ 未开启（提醒通知收不到）"
    }

    private fun isAccessibilityEnabled(): Boolean {
        val am = getSystemService(Context.ACCESSIBILITY_SERVICE) as AccessibilityManager
        val enabled = am.getEnabledAccessibilityServiceList(
            android.accessibilityservice.AccessibilityServiceInfo.FEEDBACK_ALL_MASK
        )
        val expected = "$packageName/${ControlService::class.java.name}"
        return enabled.any { it.resolveInfo.serviceInfo.packageName == packageName }
    }

    private fun isIgnoringBatteryOptimizations(): Boolean {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.M) return true
        val pm = getSystemService(Context.POWER_SERVICE) as PowerManager
        return pm.isIgnoringBatteryOptimizations(packageName)
    }

    private fun areNotificationsEnabled(): Boolean {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.N) return true
        val nm = getSystemService(Context.NOTIFICATION_SERVICE) as android.app.NotificationManager
        return nm.areNotificationsEnabled()
    }
}
