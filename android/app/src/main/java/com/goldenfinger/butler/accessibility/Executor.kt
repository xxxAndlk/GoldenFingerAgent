package com.goldenfinger.butler.accessibility

import android.accessibilityservice.AccessibilityService
import android.accessibilityservice.GestureDescription
import android.content.Intent
import android.graphics.Path
import android.graphics.Rect
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import android.util.Base64
import android.util.Log
import android.view.accessibility.AccessibilityNodeInfo
import org.json.JSONObject
import java.io.ByteArrayOutputStream

/** 一次动作的执行结果（协议 result 帧的 ok/error/data）。 */
data class ActionResult(val ok: Boolean, val error: String = "", val data: JSONObject = JSONObject())

/**
 * Executor：执行器，把 WS command 的 action 落到系统能力上。
 * action 固定取值（device-ws-contract）：
 * click/longClick/setText/scrollForward/scrollBackward/gestureTap/gestureSwipe/
 * globalBack/globalHome/globalRecents/openApp/screenshot。
 * 每个动作执行前过 SafetyGuard（敏感拦截/FLAG_SECURE 停手/急停 10s）。
 */
class Executor(
    private val service: AccessibilityService,
    private val guard: SafetyGuard
) {
    private val handler = Handler(Looper.getMainLooper())
    private val dumper = NodeDumper()

    /** 触发全局急停（内部调用 SafetyGuard，10 秒拒绝动作）。 */
    fun emergencyStop() {
        guard.emergency()
    }

    /** 执行一条 command；返回 result 帧内容。 */
    fun execute(action: String, params: JSONObject): ActionResult {
        // 急停窗口 / 敏感动作：统一在 SafetyGuard 前闸
        if (!guard.allowAction(action, params)) {
            return ActionResult(false, guard.lastBlockReason() ?: "需人工确认")
        }
        // 悬浮条显示当前动作（阶段2 可视化）
        OverlayManager.showAction(service, action)
        val result = when (action) {
            "click" -> click(params)
            "longClick" -> longClick(params)
            "setText" -> setText(params)
            "scrollForward" -> scroll(params, forward = true)
            "scrollBackward" -> scroll(params, forward = false)
            "gestureTap" -> gestureTap(params)
            "gestureSwipe" -> gestureSwipe(params)
            "globalBack" -> global(AccessibilityService.GLOBAL_ACTION_BACK)
            "globalHome" -> global(AccessibilityService.GLOBAL_ACTION_HOME)
            "globalRecents" -> global(AccessibilityService.GLOBAL_ACTION_RECENTS)
            "openApp" -> openApp(params)
            "screenshot" -> screenshot()
            else -> ActionResult(false, "未知动作: $action")
        }
        // 动作后清空节点缓存，强制下次重新 dump
        dumper.clearCache()
        return result
    }

    // ---- 节点操作（performAction，按 node_index 或坐标） ----

    private fun click(params: JSONObject): ActionResult {
        val node = nodeFrom(params)
        if (node != null) {
            return if (node.performAction(AccessibilityNodeInfo.ACTION_CLICK)) ActionResult(true)
            else ActionResult(false, "点击失败")
        }
        return gestureTap(params)
    }

    private fun longClick(params: JSONObject): ActionResult {
        val node = nodeFrom(params)
        if (node != null) {
            return if (node.performAction(AccessibilityNodeInfo.ACTION_LONG_CLICK)) ActionResult(true)
            else ActionResult(false, "长按失败")
        }
        return gestureTap(params)
    }

    private fun setText(params: JSONObject): ActionResult {
        val node = nodeFrom(params)
        val text = params.optString("text")
        if (node != null && text.isNotEmpty()) {
            val bundle = android.os.Bundle().apply {
                putCharSequence(AccessibilityNodeInfo.ACTION_ARGUMENT_SET_TEXT_CHARSEQUENCE, text)
            }
            return if (node.performAction(AccessibilityNodeInfo.ACTION_SET_TEXT, bundle)) ActionResult(true)
            else ActionResult(false, "输入文本失败")
        }
        return ActionResult(false, "找不到可输入节点")
    }

    private fun scroll(params: JSONObject, forward: Boolean): ActionResult {
        val node = nodeFrom(params)
        val action = if (forward) AccessibilityNodeInfo.ACTION_SCROLL_FORWARD
        else AccessibilityNodeInfo.ACTION_SCROLL_BACKWARD
        return if (node != null && node.performAction(action)) ActionResult(true)
        else ActionResult(false, "滚动失败")
    }

    private fun nodeFrom(params: JSONObject): AccessibilityNodeInfo? {
        if (params.has("node_index")) {
            val idx = params.getInt("node_index")
            val root = service.rootInActiveWindow ?: return null
            // 每次操作都重新 dump 以拿到最新缓存（节点可能已变化）
            dumper.dump(root)
            return dumper.nodeAt(idx)
        }
        return null
    }

    // ---- 手势（dispatchGesture 兜底） ----

    private fun gestureTap(params: JSONObject): ActionResult {
        val x = params.optInt("x", -1)
        val y = params.optInt("y", -1)
        if (x < 0 || y < 0) return ActionResult(false, "缺少坐标")
        val path = Path().apply { moveTo(x.toFloat(), y.toFloat()) }
        val g = GestureDescription.Builder()
            .addStroke(GestureDescription.StrokeDescription(path, 0, 60))
            .build()
        return dispatch(g)
    }

    private fun gestureSwipe(params: JSONObject): ActionResult {
        val x1 = params.optInt("x1", -1); val y1 = params.optInt("y1", -1)
        val x2 = params.optInt("x2", -1); val y2 = params.optInt("y2", -1)
        val dur = params.optLong("duration_ms", 300)
        if (x1 < 0 || y1 < 0 || x2 < 0 || y2 < 0) return ActionResult(false, "缺少坐标")
        val path = Path().apply {
            moveTo(x1.toFloat(), y1.toFloat())
            lineTo(x2.toFloat(), y2.toFloat())
        }
        val g = GestureDescription.Builder()
            .addStroke(GestureDescription.StrokeDescription(path, 0, dur))
            .build()
        return dispatch(g)
    }

    private fun dispatch(g: GestureDescription): ActionResult {
        var ok = false
        val done = java.util.concurrent.CountDownLatch(1)
        handler.post {
            ok = service.dispatchGesture(g, object : AccessibilityService.GestureResultCallback() {
                override fun onCompleted(gestureDescription: GestureDescription?) { done.countDown() }
                override fun onCancelled(gestureDescription: GestureDescription?) { done.countDown() }
            }, null)
        }
        try { done.await(3, java.util.concurrent.TimeUnit.SECONDS) } catch (_: InterruptedException) {}
        return if (ok) ActionResult(true) else ActionResult(false, "手势派发失败")
    }

    // ---- 全局键 ----

    private fun global(action: Int): ActionResult =
        if (service.performGlobalAction(action)) ActionResult(true) else ActionResult(false, "全局键失败")

    // ---- 打开应用 ----

    private fun openApp(params: JSONObject): ActionResult {
        val pkg = params.optString("package")
        if (pkg.isEmpty()) return ActionResult(false, "缺少 package")
        val pm = service.packageManager
        val intent = pm.getLaunchIntentForPackage(pkg)
        if (intent == null) return ActionResult(false, "未安装该应用: $pkg")
        intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        try {
            service.startActivity(intent)
            return ActionResult(true)
        } catch (t: Throwable) {
            return ActionResult(false, "打开应用失败: ${t.message}")
        }
    }

    // ---- 截图（API 30+，FLAG_SECURE 页返回 null） ----

    /** 截取当前屏幕为 JPEG base64；FLAG_SECURE/失败返回 null。 */
    fun takeScreenshotB64(): String? {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.R) return null
        var result: String? = null
        val done = java.util.concurrent.CountDownLatch(1)
        handler.post {
            try {
                service.takeScreenshot(
                    AccessibilityService.ScreenshotResult.TYPE_FULLSCREEN,
                    object : AccessibilityService.TakeScreenshotCallback {
                        override fun onSuccess(screenshot: AccessibilityService.ScreenshotResult) {
                            try {
                                val hw = screenshot.hardwareBuffer
                                val bmp = android.graphics.Bitmap.wrapHardwareBuffer(hw, null)
                                if (bmp != null) {
                                    val bos = ByteArrayOutputStream()
                                    bmp.compress(android.graphics.Bitmap.CompressFormat.JPEG, 70, bos)
                                    result = Base64.encodeToString(bos.toByteArray(), Base64.NO_WRAP)
                                }
                            } finally {
                                screenshot.hardwareBuffer.close()
                                done.countDown()
                            }
                        }
                        override fun onFailure(errorCode: Int) { done.countDown() }
                    }
                )
            } catch (t: Throwable) {
                done.countDown()
            }
        }
        try { done.await(3, java.util.concurrent.TimeUnit.SECONDS) } catch (_: InterruptedException) {}
        return result
    }

    private fun screenshot(): ActionResult {
        val b64 = takeScreenshotB64()
        if (b64 == null) return ActionResult(false, "截图失败（可能是 FLAG_SECURE 页面）")
        return ActionResult(true, data = JSONObject().put("screenshot_b64", b64))
    }
}
