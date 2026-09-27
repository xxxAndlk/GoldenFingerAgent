package com.goldenfinger.butler.accessibility

import android.accessibilityservice.AccessibilityService
import android.content.Intent
import android.os.Build
import android.util.Log
import android.view.KeyEvent
import android.view.accessibility.AccessibilityEvent

/**
 * ControlService：无障碍服务主入口（阶段0/1/2 的总控）。
 * 能力声明见 res/xml/accessibility_service_config.xml：
 * canRetrieveWindowContent + canTakeScreenshot(API30+) + canPerformGestures
 * + flagRequestFilterKeyEvents（音量键急停）。
 */
class ControlService : AccessibilityService() {

    private lateinit var dumper: NodeDumper
    private lateinit var reporter: ScreenReporter
    private lateinit var deviceClient: DeviceClient
    private lateinit var executor: Executor
    private val keyEmergency = KeyEmergency()

    override fun onServiceConnected() {
        super.onServiceConnected()
        ControlServiceHolder.service = this
        dumper = NodeDumper()
        executor = Executor(this, SafetyGuard(this))
        reporter = ScreenReporter(this, dumper, executor)
        deviceClient = DeviceClient(this, executor, reporter)
        deviceClient.connect()
        // 连接后立刻上报首帧
        reporter.schedule()
        // 阶段2：悬浮条（用户授权后显示）
        OverlayManager.init(this)
        Log.i(TAG, "无障碍服务已连接")
    }

    override fun onAccessibilityEvent(event: AccessibilityEvent) {
        when (event.eventType) {
            AccessibilityEvent.TYPE_WINDOW_CONTENT_CHANGED,
            AccessibilityEvent.TYPE_WINDOW_STATE_CHANGED,
            AccessibilityEvent.TYPE_VIEW_CLICKED,
            AccessibilityEvent.TYPE_VIEW_TEXT_CHANGED -> {
                reporter.schedule()
            }
        }
    }

    override fun onInterrupt() {
        Log.w(TAG, "onInterrupt")
    }

    override fun onDestroy() {
        ControlServiceHolder.service = null
        deviceClient.close()
        OverlayManager.hide()
        super.onDestroy()
    }

    override fun onKeyEvent(event: KeyEvent): Boolean {
        if (keyEmergency.onKeyEvent(event, this)) {
            return true
        }
        return super.onKeyEvent(event)
    }

    /** 触发全局急停（音量键/悬浮条共用入口），急停后 10 秒拒绝动作。 */
    fun keyEmergencyForStop() {
        executor.emergencyStop()
        OverlayManager.showStopped()
        Log.w(TAG, "全局急停已触发")
    }

    companion object {
        private const val TAG = "ControlService"
        const val ACTION_STOP = "com.goldenfinger.butler.action.STOP"

        /** 从任意入口触发急停（悬浮条停止按钮 / 通知）。 */
        fun requestStop() {
            OverlayManager.emergencyStop()
        }
    }
}
