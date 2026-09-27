package com.goldenfinger.butler

import android.app.Application

/** Application：初始化全局实例，供无 Context 场景取用。 */
class App : Application() {
    override fun onCreate() {
        super.onCreate()
        instance = this
    }

    companion object {
        lateinit var instance: App
            private set
    }
}
