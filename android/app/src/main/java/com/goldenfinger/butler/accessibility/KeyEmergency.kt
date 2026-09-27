package com.goldenfinger.butler.accessibility

import android.util.Log
import android.view.KeyEvent

/**
 * KeyEmergency：音量键急停。
 * 连续 3 次音量键（间隔 <3s）或长按音量键 ≥1s → 触发全局急停 10 秒。
 * 依赖 AccessibilityService 的 flagRequestFilterKeyEvents 收到 onKeyEvent。
 */
class KeyEmergency {
    private var lastPress: Long = 0
    private var pressCount = 0
    private var pressDownAt: Long = 0
    private var handled = false

    /** 返回 true 表示已消费该按键事件。 */
    fun onKeyEvent(event: KeyEvent, service: ControlService): Boolean {
        val isVolume = event.keyCode == KeyEvent.KEYCODE_VOLUME_UP ||
            event.keyCode == KeyEvent.KEYCODE_VOLUME_DOWN
        if (!isVolume) return false

        when (event.action) {
            KeyEvent.ACTION_DOWN -> {
                pressDownAt = System.currentTimeMillis()
                handled = false
            }
            KeyEvent.ACTION_UP -> {
                val held = System.currentTimeMillis() - pressDownAt
                if (held >= 1000 && !handled) {
                    // 长按急停
                    handled = true
                    trigger(service)
                    return true
                }
                // 连续短按计数
                val now = System.currentTimeMillis()
                pressCount = if (now - lastPress < 3000) pressCount + 1 else 1
                lastPress = now
                if (pressCount >= 3) {
                    pressCount = 0
                    trigger(service)
                    return true
                }
            }
        }
        return false
    }

    private fun trigger(service: ControlService) {
        Log.w(TAG, "音量键急停触发")
        service.keyEmergencyForStop()
    }

    companion object {
        private const val TAG = "KeyEmergency"
    }
}
