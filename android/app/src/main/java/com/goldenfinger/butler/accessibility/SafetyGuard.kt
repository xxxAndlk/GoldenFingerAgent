package com.goldenfinger.butler.accessibility

import android.content.Context
import android.os.SystemClock
import org.json.JSONObject
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/**
 * SafetyGuard（能力层，不再承载业务规则）。
 *
 * 业务判定已经搬到 Go 侧（internal/device/guard.go）：敏感词、敏感包名、
 * 是否需要人工确认，都由服务端决定并通过 `confirm` 动作下发。
 *
 * 这里只保留两件必须在设备本地完成的事：
 *  1) 急停计时：音量键/悬浮条触发后，10 秒内本地直接拒绝一切动作。
 *     这一层必须独立于网络——断网、服务端卡死时也要能立刻停下。
 *  2) 确认 UI：把服务端给的提示显示出来，并把「允许/拒绝」原样回传。
 *
 * 不要在 Kotlin 里增加敏感词表或“这是不是支付页”的判断：
 * 那会让红线逻辑分裂在两处，无法用 Go 测试覆盖。
 */
class SafetyGuard(@Suppress("UNUSED_PARAMETER") ctx: Context) {

    /** 急停时间戳（elapsedRealtime ms）；0 表示未急停。 */
    @Volatile
    private var emergencyUntil: Long = 0

    /** 最近一次本地拦截原因（供 result.error）。 */
    @Volatile
    private var lastBlock: String? = null

    fun lastBlockReason(): String? = lastBlock

    /** 触发急停（音量键/悬浮条），持续 10 秒。 */
    fun emergency() {
        emergencyUntil = SystemClock.elapsedRealtime() + 10_000
        OverlayManager.showStopped()
        lastBlock = "已急停，10 秒内不再自动操作"
    }

    fun isEmergency(): Boolean = SystemClock.elapsedRealtime() < emergencyUntil

    /**
     * 本地硬闸：只检查急停。真正的敏感判定在 Go 侧。
     * 返回 false 时 [lastBlockReason] 给出原因。
     */
    fun allowLocal(): Boolean {
        lastBlock = null
        if (isEmergency()) {
            lastBlock = "急停中，请稍候（10 秒）"
            return false
        }
        return true
    }

    /**
     * 请求人工确认：纯 UI 能力，把用户的选择如实返回。
     * 无悬浮窗权限或超时 → 返回 false（安全默认拒绝）。
     */
    @Suppress("UNUSED_PARAMETER")
    fun confirm(message: String, timeoutMillis: Long = 30_000): Boolean {
        val gate = CountDownLatch(1)
        val result = booleanArrayOf(false)
        OverlayManager.showConfirm(message) { yes ->
            result[0] = yes
            gate.countDown()
        }
        return try {
            if (gate.await(timeoutMillis, TimeUnit.MILLISECONDS)) result[0] else false
        } catch (_: InterruptedException) {
            false
        }
    }

    /** FLAG_SECURE 页面停手：由 Executor 截图失败后调用，给出人话。 */
    fun securePageError(): String = "这一步请您自己点（当前页面禁止自动操作）"

    /** 兼容旧调用点：只保留本地急停判定，敏感判定交给服务端。 */
    @Deprecated("敏感判定已搬到 Go 侧；Executor 应使用 allowLocal()")
    fun allowAction(action: String, params: JSONObject): Boolean {
        @Suppress("UNUSED_EXPRESSION") action
        @Suppress("UNUSED_EXPRESSION") params
        return allowLocal()
    }
}
