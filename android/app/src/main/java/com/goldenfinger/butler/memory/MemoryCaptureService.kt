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
import com.goldenfinger.butler.MainActivity
import com.goldenfinger.butler.R
import java.util.Locale

/**
 * 持续听写服务：识别最终语句，附上最近上下文后写入本机情景记忆。
 * 原始音频只交给系统 SpeechRecognizer，不写入 App 文件，也不发送到 Go 服务。
 */
class MemoryCaptureService : Service(), RecognitionListener {
    private val handler = Handler(Looper.getMainLooper())
    private var recognizer: SpeechRecognizer? = null
    private var destroyed = false
    private var pending: PendingSegment? = null
    private val recent = java.util.ArrayDeque<RecentSegment>()
    private var tts: TextToSpeech? = null
    private var speaking = false
    private var awaitingCommandUntil = 0L

    private val restartRunnable = Runnable { startListening() }
    private val flushRunnable = Runnable { flushPending("") }

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
        if (text.isNotEmpty()) handleSpokenText(text)
        scheduleRestart(250)
    }

    private fun handleSpokenText(text: String) {
        val now = System.currentTimeMillis()
        val compact = text.replace(" ", "")
        val wakeAt = compact.indexOf(WAKE_WORD)
        if (wakeAt >= 0) {
            flushPending("")
            val command = compact.substring(wakeAt + WAKE_WORD.length)
                .trimStart('，', ',', '。', '.', '：', ':')
            if (command.isEmpty()) {
                awaitingCommandUntil = now + COMMAND_WINDOW_MS
                speak("我在")
            } else {
                awaitingCommandUntil = 0
                acceptFinal(command)
                askAssistant(command)
            }
            return
        }
        if (now < awaitingCommandUntil) {
            awaitingCommandUntil = 0
            acceptFinal(text)
            askAssistant(text)
            return
        }
        acceptFinal(text)
    }

    private fun askAssistant(command: String) {
        MemoryCaptureApi.chat(command) { reply ->
            handler.post { if (!destroyed) speak(reply) }
        }
    }

    private fun speak(text: String) {
        speaking = true
        handler.removeCallbacks(restartRunnable)
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

    private fun acceptFinal(text: String) {
        // 下一句很快到来时，它同时是上一句的后文；否则 4 秒后保存上一句。
        flushPending(text)
        val now = System.currentTimeMillis()
        while (recent.isNotEmpty() && now - recent.first.atMillis > CONTEXT_WINDOW_MS) {
            recent.removeFirst()
        }
        val before = recent.toList().takeLast(4).joinToString("。") { it.text }
        pending = PendingSegment(text, before, now)
        handler.removeCallbacks(flushRunnable)
        handler.postDelayed(flushRunnable, CONTEXT_WAIT_MS)
    }

    private fun flushPending(after: String) {
        val segment = pending ?: return
        handler.removeCallbacks(flushRunnable)
        pending = null
        MemoryCaptureApi.capture(segment.text, segment.before, after, segment.atMillis)
        recent.addLast(RecentSegment(segment.text, segment.atMillis))
        while (recent.size > 6) recent.removeFirst()
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
        flushPending("")
        recognizer?.stopListening()
        recognizer?.destroy()
        recognizer = null
        tts?.stop()
        tts?.shutdown()
        tts = null
        super.onDestroy()
    }

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

    private data class PendingSegment(val text: String, val before: String, val atMillis: Long)
    private data class RecentSegment(val text: String, val atMillis: Long)

    companion object {
        private const val TAG = "MemoryCaptureService"
        private const val CHANNEL_ID = "memory_capture"
        private const val NOTIFICATION_ID = 1003
        private const val CONTEXT_WAIT_MS = 4000L
        private const val CONTEXT_WINDOW_MS = 30_000L
        private const val COMMAND_WINDOW_MS = 10_000L
        private const val WAKE_WORD = "小金小金"
        private const val PREF_NAME = "memory_capture"
        private const val KEY_ENABLED = "enabled"
        private const val ACTION_STOP = "com.goldenfinger.butler.memory.STOP"

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
