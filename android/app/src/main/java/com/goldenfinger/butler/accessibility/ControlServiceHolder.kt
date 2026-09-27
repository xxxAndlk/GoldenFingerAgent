package com.goldenfinger.butler.accessibility

/** ControlServiceHolder：给 ScreenReporter 等持有 Service 实例（Service 生命周期内有效）。 */
object ControlServiceHolder {
    @Volatile var service: ControlService? = null
}
