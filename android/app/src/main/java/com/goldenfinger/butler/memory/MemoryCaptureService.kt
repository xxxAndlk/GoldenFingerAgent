package com.goldenfinger.butler.memory

import android.Manifest
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.speech.RecognitionListener
import android.speech.RecognizerIntent
import android.speech.SpeechRecognizer
import android.speech.tts.TextToSpeech
import android.speech.tts.UtteranceProgressListener
import android.util.Log
import androidx.core.app.NotificationCompat
import androidx.core.content.ContextCompat
import com.goldenfinger.butler.GoBridge
import com.goldenfinger.butler.MainActivity
import com.goldenfinger.butler.R
import java.util.Locale

/**
 * 持续听写服务。
 *
 * 这里只做「能力」：系统语音识别、语音播报、前台通知与重连退避。
 * 唤醒词、指令窗口、片段合并、上下文窗口等判定全部在 Go
 * （`POST /api/voice/text`），本服务只执行 Go 返回的动作。
 * 原始音频只交给系统 SpeechRecognizer，不写入 App 文件，也不发送到 Go 服务。
 */
class MemoryCaptureService : Service(), RecognitionListener {
    private val handler = Handler(Looper.getMainLooper())
    private var recognizer: SpeechRecognizer? = null
    private var destroyed = false
    private var tts: TextToSpeech? = null
    private var speaking = false

    private val restartRunnable = Runnable { startListening() }
    /** 等待后文超时后请 Go 收尾当前片段（时长由 Go 的 next_flush_ms 决定）。 */
    private val flushRunnable = Runnable { requestFlush() }

    override fun onCreate() {
        super.onCreate()
        startForegroundCompat()
        if (ContextCompat.checkSelfPermission(this, Manifest.permission.RECORD_AUDIO) != PackageManager.PERMISSION_GRANTED) {
            stopSelf()
            return
        }
        if (!SpeechRecognizer.isRecognitionAvailable(this)) {
            Log.w(TAG, "system speech recognition is unavailable")
            stopSelf()
            return
        }
        recognizer = SpeechRecognizer.createSpeechRecognizer(this).also {
            it.setRecognitionListener(this)
        }
        tts = TextToSpeech(this) { status ->
            if (status == TextToSpeech.SUCCESS) tts?.setLanguage(Locale.SIMPLIFIED_CHINESE)
        }
        tts?.setOnUtteranceProgressListener(object : UtteranceProgressListener() {
            override fun onStart(utteranceId: String?) = Unit
            override fun onDone(utteranceId: String?) = finishSpeaking()
            override fun onError(utteranceId: String?) = finishSpeaking()
        })
        startListening()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            prefs().edit().putBoolean(KEY_ENABLED, false).apply()
            stopSelf()
            return START_NOT_STICKY
        }
        return START_STICKY
    }

    override fun onBind(intent: Intent?): IBinder? = null

    private fun startListening() {
        val sr = recognizer ?: return
        if (destroyed) return
        val intent = Intent(RecognizerIntent.ACTION_RECOGNIZE_SPEECH).apply {
            putExtra(RecognizerIntent.EXTRA_LANGUAGE_MODEL, RecognizerIntent.LANGUAGE_MODEL_FREE_FORM)
            putExtra(RecognizerIntent.EXTRA_LANGUAGE, "zh-CN")
            putExtra(RecognizerIntent.EXTRA_PARTIAL_RESULTS, true)
            putExtra(RecognizerIntent.EXTRA_MAX_RESULTS, 1)
        }
        try {
            sr.startListening(intent)
        } catch (t: Throwable) {
            Log.w(TAG, "startListening failed: ${t.message}")
            scheduleRestart(2000)
        }
    }

    private fun scheduleRestart(delayMillis: Long) {
        if (destroyed || speaking) return
        handler.removeCallbacks(restartRunnable)
        handler.postDelayed(restartRunnable, delayMillis)
    }

    override fun onResults(results: Bundle?) {
        if (speaking) return
        val text = results
            ?.getStringArrayList(SpeechRecognizer.RESULTS_RECOGNITION)
            ?.firstOrNull()
            ?.trim()
            .orEmpty()
        if (text.isNotEmpty()) {
            handler.removeCallbacks(flushRunnable)
            MemoryCaptureApi.onText(deviceId(), text, flush = false) { verdict ->
                handler.post { if (!destroyed) handleVerdict(verdict) }
            }
        }
        scheduleRestart(250)
    }

    /** 执行 Go 判定出的动作；capture 已由 Go 落库，这里无需处理。 */
    private fun handleVerdict(verdict: MemoryCaptureApi.Verdict) {
        var spoken = false
        for (action in verdict.actions) {
            when (action.type) {
                ACTION_SPEAK -> if (action.text.isNotEmpty()) {
                    speak(action.text)
                    spoken = true
                }
                ACTION_CHAT -> if (action.text.isNotEmpty()) {
                    MemoryCaptureApi.chat(action.text) { reply ->
                        handler.post { if (!destroyed) speak(reply) }
                    }
                    spoken = true
                }
            }
        }
        // 没有需要播报的动作时，等 Go 给的合并时限后再请它收尾当前片段。
        if (!spoken && !destroyed) {
            handler.removeCallbacks(flushRunnable)
            handler.postDelayed(flushRunnable, verdict.nextFlushMs)
        }
    }

    private fun requestFlush() {
        if (destroyed) return
        MemoryCaptureApi.onText(deviceId(), text = "", flush = true) { verdict ->
            handler.post { if (!destroyed) handleVerdict(verdict) }
        }
    }

    private fun speak(text: String) {
        speaking = true
        handler.removeCallbacks(restartRunnable)
        handler.removeCallbacks(flushRunnable)
        recognizer?.stopListening()
        val result = tts?.speak(text, TextToSpeech.QUEUE_FLUSH, null, "assistant-reply")
        if (result != TextToSpeech.SUCCESS) finishSpeaking()
    }

    private fun finishSpeaking() {
        handler.post {
            speaking = false
            scheduleRestart(250)
        }
    }

    override fun onError(error: Int) {
        if (error == SpeechRecognizer.ERROR_INSUFFICIENT_PERMISSIONS) {
            prefs().edit().putBoolean(KEY_ENABLED, false).apply()
            stopSelf()
            return
        }
        val delay = when (error) {
            SpeechRecognizer.ERROR_RECOGNIZER_BUSY -> 2000L
            SpeechRecognizer.ERROR_NETWORK,
            SpeechRecognizer.ERROR_NETWORK_TIMEOUT,
            SpeechRecognizer.ERROR_SERVER -> 3000L
            else -> 500L
        }
        scheduleRestart(delay)
    }

    override fun onDestroy() {
        destroyed = true
        handler.removeCallbacks(restartRunnable)
        // 退出前把暂存片段交给 Go 收尾，避免丢掉最后一句。
        MemoryCaptureApi.onText(deviceId(), text = "", flush = true) { }
        handler.removeCallbacks(flushRunnable)
        recognizer?.stopListening()
        recognizer?.destroy()
        recognizer = null
        tts?.stop()
        tts?.shutdown()
        tts = null
        super.onDestroy()
    }

    private fun deviceId(): String = GoBridge.deviceId().orEmpty()

    private fun startForegroundCompat() {
        val nm = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            nm.createNotificationChannel(
                NotificationChannel(CHANNEL_ID, "情景记忆", NotificationManager.IMPORTANCE_LOW).apply {
                    description = "持续听写运行状态与停止入口"
                }
            )
        }
        val open = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
        val stop = PendingIntent.getService(
            this, 1, Intent(this, MemoryCaptureService::class.java).setAction(ACTION_STOP),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE
        )
        val notification = NotificationCompat.Builder(this, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_stat_butler)
            .setContentTitle("正在记录随口说的话")
            .setContentText("只保存转写片段，可随时停止")
            .setContentIntent(open)
            .setOngoing(true)
            .addAction(0, "停止记录", stop)
            .build()
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            startForeground(NOTIFICATION_ID, notification, ServiceInfo.FOREGROUND_SERVICE_TYPE_MICROPHONE)
        } else {
            startForeground(NOTIFICATION_ID, notification)
        }
    }

    private fun prefs() = getSharedPreferences(PREF_NAME, Context.MODE_PRIVATE)

    override fun onReadyForSpeech(params: Bundle?) = Unit
    override fun onBeginningOfSpeech() = Unit
    override fun onRmsChanged(rmsdB: Float) = Unit
    override fun onBufferReceived(buffer: ByteArray?) = Unit
    override fun onEndOfSpeech() = Unit
    override fun onPartialResults(partialResults: Bundle?) = Unit
    override fun onEvent(eventType: Int, params: Bundle?) = Unit

    companion object {
        private const val TAG = "MemoryCaptureService"
        private const val CHANNEL_ID = "memory_capture"
        private const val NOTIFICATION_ID = 1003
        private const val PREF_NAME = "memory_capture"
        private const val KEY_ENABLED = "enabled"
        private const val ACTION_STOP = "com.goldenfinger.butler.memory.STOP"

        // 动作类型与 Go 侧 internal/voice 保持一致。
        private const val ACTION_SPEAK = "speak"
        private const val ACTION_CHAT = "chat"

        /** 用户授权麦克风后调用，表示明确开启持续记录。 */
        fun enableAndStart(context: Context) {
            context.getSharedPreferences(PREF_NAME, Context.MODE_PRIVATE)
                .edit().putBoolean(KEY_ENABLED, true).apply()
            startService(context)
        }

        /** App 再次打开时恢复用户此前选择；通知栏停止后不会自动重启。 */
        fun startIfEnabled(context: Context) {
            val prefs = context.getSharedPreferences(PREF_NAME, Context.MODE_PRIVATE)
            if (!prefs.contains(KEY_ENABLED) || prefs.getBoolean(KEY_ENABLED, false)) {
                enableAndStart(context)
            }
        }

        fun isEnabled(context: Context): Boolean =
            context.getSharedPreferences(PREF_NAME, Context.MODE_PRIVATE)
                .getBoolean(KEY_ENABLED, false)

        fun stop(context: Context) {
            context.getSharedPreferences(PREF_NAME, Context.MODE_PRIVATE)
                .edit().putBoolean(KEY_ENABLED, false).apply()
            context.stopService(Intent(context, MemoryCaptureService::class.java))
        }

        private fun startService(context: Context) {
            val intent = Intent(context, MemoryCaptureService::class.java)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                context.startForegroundService(intent)
            } else {
                context.startService(intent)
            }
        }
    }
}
