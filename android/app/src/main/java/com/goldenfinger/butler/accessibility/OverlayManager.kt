package com.goldenfinger.butler.accessibility

import android.content.Context
import android.graphics.Color
import android.graphics.PixelFormat
import android.graphics.Typeface
import android.os.Build
import android.provider.Settings
import android.view.Gravity
import android.view.View
import android.view.WindowManager
import android.widget.Button
import android.widget.LinearLayout
import android.widget.TextView

/**
 * OverlayManager：阶段2 悬浮条。
 * TYPE_APPLICATION_OVERLAY 显示「AI 正在操作：<当前动作>」+ 红色「停止」按钮；
 * 点停止触发全局急停（emergency 10s）。无悬浮窗权限时静默不可用。
 */
object OverlayManager {
    private var wm: WindowManager? = null
    private var view: View? = null
    private var actionLabel: TextView? = null

    fun init(ctx: Context) {
        if (wm != null) return
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.M && !Settings.canDrawOverlays(ctx)) return
        wm = ctx.getSystemService(Context.WINDOW_SERVICE) as WindowManager
    }

    /** 有悬浮窗权限才可用（确认条依赖悬浮窗）。 */
    fun available(ctx: Context): Boolean =
        Build.VERSION.SDK_INT < Build.VERSION_CODES.M || Settings.canDrawOverlays(ctx)

    /** 显示/更新当前动作文本。 */
    fun showAction(ctx: Context, text: String) {
        ensureView(ctx)
        actionLabel?.text = "AI 正在操作：$text"
    }

    /** 急停后显示红色状态。 */
    fun showStopped() {
        actionLabel?.text = "⛔ 已急停（10 秒）"
        actionLabel?.setTextColor(Color.parseColor("#E53935"))
    }

    /** 敏感操作确认条：显示消息与「允许/拒绝」，回调结果。 */
    fun showConfirm(message: String, cb: (Boolean) -> Unit) {
        val ctx = ControlServiceHolder.service ?: return
        ensureView(ctx)
        actionLabel?.text = "⚠️ 需要您确认：$message"
        actionLabel?.setTextColor(Color.parseColor("#FFD54F"))
        val yes = Button(ctx).apply {
            text = "允许"
            setTextColor(Color.WHITE)
            setBackgroundColor(Color.parseColor("#2E7D32"))
            textSize = 13f
            setOnClickListener { cb(true); dismissConfirm() }
        }
        val no = Button(ctx).apply {
            text = "拒绝"
            setTextColor(Color.WHITE)
            setBackgroundColor(Color.parseColor("#E53935"))
            textSize = 13f
            setOnClickListener { cb(false); dismissConfirm() }
        }
        (view as? LinearLayout)?.let { row ->
            row.removeAllViews()
            row.addView(actionLabel, LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f))
            row.addView(no, LinearLayout.LayoutParams(140, LinearLayout.LayoutParams.WRAP_CONTENT))
            row.addView(yes, LinearLayout.LayoutParams(140, LinearLayout.LayoutParams.WRAP_CONTENT))
        }
    }

    private fun dismissConfirm() {
        actionLabel?.text = "AI 正在操作…"
        actionLabel?.setTextColor(Color.WHITE)
    }

    fun hide() {
        view?.let { runCatching { wm?.removeView(it) } }
        view = null
        wm = null
        actionLabel = null
    }

    /** 悬浮条停止按钮 → 全局急停。 */
    fun emergencyStop() {
        ControlServiceHolder.service?.let {
            // 通过服务内 SafetyGuard 触发（音量键同一入口）
            it.keyEmergencyForStop()
        }
    }

    private fun ensureView(ctx: Context) {
        if (view != null) return
        val label = TextView(ctx).apply {
            text = "AI 正在操作…"
            setTextColor(Color.WHITE)
            textSize = 14f
            typeface = Typeface.DEFAULT_BOLD
        }
        actionLabel = label
        val stop = Button(ctx).apply {
            text = "停止"
            setTextColor(Color.WHITE)
            setBackgroundColor(Color.parseColor("#E53935"))
            textSize = 13f
            setOnClickListener { emergencyStop() }
        }
        val row = LinearLayout(ctx).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(24, 12, 24, 12)
            setBackgroundColor(Color.parseColor("#B3000000"))
            addView(label, LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f))
            addView(stop, LinearLayout.LayoutParams(160, LinearLayout.LayoutParams.WRAP_CONTENT))
        }
        val type = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O)
            WindowManager.LayoutParams.TYPE_APPLICATION_OVERLAY
        else
            @Suppress("DEPRECATION") WindowManager.LayoutParams.TYPE_PHONE
        val params = WindowManager.LayoutParams(
            WindowManager.LayoutParams.MATCH_PARENT,
            WindowManager.LayoutParams.WRAP_CONTENT,
            type,
            WindowManager.LayoutParams.FLAG_NOT_FOCUSABLE or WindowManager.LayoutParams.FLAG_NOT_TOUCH_MODAL,
            PixelFormat.TRANSLUCENT
        ).apply {
            gravity = Gravity.TOP
            y = 200
        }
        try {
            wm?.addView(row, params)
            view = row
        } catch (_: Throwable) {
            // 权限未授予等，忽略
        }
    }
}
