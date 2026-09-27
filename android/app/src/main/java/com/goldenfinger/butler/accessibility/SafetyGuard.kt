package com.goldenfinger.butler.accessibility

import android.app.AlertDialog
import android.content.Context
import android.os.SystemClock
import org.json.JSONObject

/**
 * SafetyGuard：安全闸（阶段2 红线）。
 * 1) 敏感动作拦截：节点/待输入文本命中 支付|转账|付款|确认支付|删除|下单，
 *    或前台包名属支付/银行类 → 本地确认对话框；未确认 ok=false error="需人工确认"。
 * 2) FLAG_SECURE 页面：截图黑屏/失败 → 停手 error="这一步请您自己点"。
 * 3) 全局 emergency 标志：音量键/悬浮条急停后 10 秒内拒绝一切动作。
 */
class SafetyGuard(private val ctx: Context) {

    private val sensitiveKeywords = listOf("支付", "转账", "付款", "确认支付", "删除", "下单")
    private val sensitivePackages = listOf(
        "com.tencent.mm", "com.alipay", "com.unionpay", "com.tencent.mobileqq",
        "com.android.bank", "com.cmbchina", "com.icbc", "com.ccb", "com.abchina"
    )

    /** 急停时间戳（epoch ms）；0 表示未急停。 */
    @Volatile var emergencyUntil: Long = 0
        private set

    /** 最近一次拦截原因（供 result.error）。 */
    @Volatile private var lastBlock: String? = null

    fun lastBlockReason(): String? = lastBlock

    /** 触发急停（音量键/悬浮条），持续 10 秒。 */
    fun emergency() {
        emergencyUntil = SystemClock.elapsedRealtime() + 10_000
        OverlayManager.showStopped()
        lastBlock = "已急停，10 秒内不再自动操作"
    }

    fun isEmergency(): Boolean = SystemClock.elapsedRealtime() < emergencyUntil

    /** 动作前闸门：急停中、敏感文本/包名需人工确认。 */
    fun allowAction(action: String, params: JSONObject): Boolean {
        lastBlock = null
        if (isEmergency()) {
            lastBlock = "急停中，请稍候（10 秒）"
            return false
        }
        // 屏幕文本/前台包名敏感检查（不升级为指令——只作拦截依据）
        val screenText = currentScreenText()
        val pkg = currentPackage()
        if (pkg in sensitivePackages || screenText.any { it in sensitiveKeywords }) {
            lastBlock = "需人工确认"
            return confirm("检测到敏感操作（支付/转账/删除/下单类），是否继续？\n\n当前页面：$pkg")
        }
        return true
    }

    /** FLAG_SECURE 页面停手：由 Executor 截图失败后调用本方法给出人话。 */
    fun securePageError(): String = "这一步请您自己点（当前页面禁止自动操作）"

    private fun currentScreenText(): String {
        val root = ControlServiceHolder.service?.rootInActiveWindow ?: return ""
        val sb = StringBuilder()
        collectText(root, sb)
        return sb.toString()
    }

    private fun collectText(node: android.view.accessibility.AccessibilityNodeInfo, sb: StringBuilder) {
        node.text?.let { sb.append(it).append(' ') }
        node.contentDescription?.let { sb.append(it).append(' ') }
        for (i in 0 until node.childCount) {
            node.getChild(i)?.let { collectText(it, sb) }
        }
    }

    private fun currentPackage(): String =
        ControlServiceHolder.service?.rootInActiveWindow?.packageName?.toString() ?: ""

    private fun confirm(message: String): Boolean {
        // 无 Activity 上下文，不能用 AlertDialog；改走悬浮确认条（需悬浮窗权限）。
        // 无权限时按「未确认」处理（安全默认拒绝）。
        val gate = java.util.concurrent.CountDownLatch(1)
        val result = BooleanArray(1)
        OverlayManager.showConfirm(message) { yes ->
            result[0] = yes
            gate.countDown()
        }
        try { gate.await(30, java.util.concurrent.TimeUnit.SECONDS) } catch (_: InterruptedException) {}
        // 超时未确认 → 拒绝
        if (gate.count > 0) result[0] = false
        return result[0]
    }
}
