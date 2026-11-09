package com.goldenfinger.butler

import android.Manifest
import android.annotation.SuppressLint
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.provider.Settings
import android.view.Gravity
import android.view.KeyEvent
import android.view.ViewGroup
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Button
import android.widget.LinearLayout
import android.widget.TextView
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity
import androidx.core.content.ContextCompat
import com.goldenfinger.butler.memory.MemoryCaptureService

/**
 * MainActivity：全屏 WebView 壳。
 * URL = http://127.0.0.1:PORT/?token=..&user_id=..（Web 页 apiFetch 读取 query
 * token/user_id 并注入 X-Token / X-User-Id，无需改服务端）。
 * JS/DOM storage 开启；settings/config/SQLite 均在 App 私有目录。
 */
class MainActivity : AppCompatActivity() {
    private lateinit var webView: WebView
    private lateinit var captureButton: Button
    // 通知点击带过来的目标面板；冷启动时拼进 URL，热启动时直接调 JS。
    private var pendingRoute = ""

    companion object {
        /** 通知 / 转述意图里携带的面板路由：brief / tasks / inbox。 */
        const val EXTRA_ROUTE = "gfa_route"

        /** 申请麦克风权限的请求码。 */
        private const val REQ_RECORD_AUDIO = 41
    }

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        // 确保服务与 Go 服务在跑（冷启动路径）
        GoForegroundService.start(this)
        KeepAliveWorker.schedule(this)
        ensureMemoryCapture()
        pendingRoute = intent?.getStringExtra(EXTRA_ROUTE).orEmpty()

        webView = WebView(this)
        setContentView(buildContentView())
        refreshCaptureControl()

        val ws = webView.settings.apply {
            javaScriptEnabled = true
            domStorageEnabled = true
            allowFileAccess = false
            mediaPlaybackRequiresUserGesture = false
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                safeBrowsingEnabled = false // 本机 localhost，避免安全浏览误报
            }
            setSupportZoom(false)
            builtInZoomControls = false
        }
        @Suppress("DEPRECATION")
        ws.userAgentString = ws.userAgentString + " GoldenFingerButler/0.1"

        // 交付物下载/校验/保存/分享在 Kotlin 完成，WebView 只提供列表。
        webView.addJavascriptInterface(DeliveryBridge(this), "gfaDelivery")

        webView.webViewClient = object : WebViewClient() {
            // 拦截所有非本机请求（本应用只访问 127.0.0.1 的 Go 服务）
            @SuppressLint("WebViewClientOnReceivedHttpError")
            override fun shouldInterceptRequest(
                view: WebView?, request: WebResourceRequest?
            ): WebResourceResponse? {
                val url = request?.url?.toString().orEmpty()
                if (url.startsWith("http://127.0.0.1") || url.startsWith("http://localhost") ||
                    url.startsWith("data:") || url.startsWith("about:")
                ) {
                    return super.shouldInterceptRequest(view, request)
                }
                // 非本机地址（如页面里误触发的外部跳转）返回空响应
                return WebResourceResponse("text/plain", "utf-8", null)
            }
        }

        loadHome()
    }

    private fun loadHome() {
        val addr = GoBridge.baseUrl()
        val token = GoBridge.token()
        if (addr.isNullOrBlank() || token.isNullOrBlank()) {
            // Go 服务尚未就绪（首次启动），稍后重试
            webView.postDelayed({ loadHome() }, 800)
            return
        }
        // 取 user_id（缓存在 GoBridge；没有则向 /api/me 拉取一次）
        val uid = GoBridge.userId()
        if (uid.isNullOrBlank()) {
            Thread {
                val id = fetchMe(addr, token)
                if (id != null) {
                    GoBridge.saveUserId(id)
                    webView.post { loadUrl(addr, token, id) }
                } else {
                    webView.post { loadUrl(addr, token, "") }
                }
            }.start()
            return
        }
        loadUrl(addr, token, uid)
    }

    override fun onNewIntent(intent: Intent?) {
        super.onNewIntent(intent)
        setIntent(intent)
        val route = intent?.getStringExtra(EXTRA_ROUTE).orEmpty()
        if (route.isBlank()) return
        pendingRoute = route
        // 已经在跑就直接切面板；还没加载页面就等下次 loadHome 拼进 URL。
        if (webView.url.isNullOrBlank()) loadHome() else deliverRoute(route)
    }

    private fun loadUrl(addr: String, token: String, uid: String) {
        val base = if (uid.isBlank()) "http://$addr/?token=$token"
        else "http://$addr/?token=$token&user_id=$uid"
        // 冷启动来自通知：把目标面板带进页面，由前端自己决定打开哪个抽屉。
        val route = pendingRoute
        pendingRoute = ""
        val url = if (route.isBlank()) base else "$base&route=${Uri.encode(route)}"
        webView.loadUrl(url)
    }

    /**
     * 热启动时切换面板。
     *
     * 页面还没挂载（gfaOpenRoute 缺失）就等一会儿再试，
     * 不能让用户点了通知却停在原来的页面。
     */
    private fun deliverRoute(route: String, attempt: Int = 0) {
        if (route.isBlank() || attempt > 20) return
        val safe = route.replace("'", "").replace("\\", "")
        val script = "(function(){return typeof window.gfaOpenRoute==='function'?window.gfaOpenRoute('$safe'):'missing';})()"
        webView.evaluateJavascript(script) { value ->
            if (value == null || value.contains("missing")) {
                webView.postDelayed({ deliverRoute(route, attempt + 1) }, 250)
            }
        }
    }

    /** 向 /api/me 取当前用户 id（X-Token 鉴权）。 */
    private fun fetchMe(addr: String, token: String): String? = try {
        val req = okhttp3.Request.Builder()
            .url("http://$addr/api/me")
            .header("X-Token", token)
            .get()
            .build()
        okhttp3.OkHttpClient().newCall(req).execute().use { resp ->
            if (!resp.isSuccessful) return null
            val j = org.json.JSONObject(resp.body?.string().orEmpty())
            j.optString("id").takeIf { it.isNotBlank() }
        }
    } catch (t: Throwable) {
        null
    }

    private fun buildContentView(): android.view.View {
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            fitsSystemWindows = true
        }
        val header = LinearLayout(this).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(dp(16), dp(4), dp(12), dp(4))
            setBackgroundColor(ContextCompat.getColor(this@MainActivity, R.color.white))
        }
        val label = TextView(this).apply {
            text = "小金小金 · 情景记忆"
            textSize = 16f
            setTextColor(ContextCompat.getColor(this@MainActivity, R.color.ink))
        }
        captureButton = Button(this).apply {
            textSize = 14f
            minHeight = dp(48)
            setOnClickListener {
                if (MemoryCaptureService.isEnabled(this@MainActivity)) {
                    MemoryCaptureService.stop(this@MainActivity)
                } else if (ContextCompat.checkSelfPermission(this@MainActivity, Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED) {
                    MemoryCaptureService.enableAndStart(this@MainActivity)
                } else {
                    requestPermissions(arrayOf(Manifest.permission.RECORD_AUDIO), REQ_RECORD_AUDIO)
                }
                refreshCaptureControl()
            }
        }
        header.addView(label, LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1f))
        header.addView(captureButton)
        root.addView(header, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT))
        root.addView(webView, LinearLayout.LayoutParams(ViewGroup.LayoutParams.MATCH_PARENT, 0, 1f))
        return root
    }

    private fun refreshCaptureControl() {
        val enabled = MemoryCaptureService.isEnabled(this)
        captureButton.text = if (enabled) "停止记录" else "开始记录"
        captureButton.contentDescription = if (enabled) "停止持续听写和情景记忆" else "开始持续听写和情景记忆"
    }

    private fun dp(value: Int): Int = (value * resources.displayMetrics.density).toInt()

    override fun onResume() {
        super.onResume()
        if (::captureButton.isInitialized) refreshCaptureControl()
    }

    private fun ensureMemoryCapture() {
        if (ContextCompat.checkSelfPermission(this, Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED) {
            MemoryCaptureService.startIfEnabled(this)
        } else {
            requestPermissions(arrayOf(Manifest.permission.RECORD_AUDIO), REQ_RECORD_AUDIO)
        }
    }

    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<out String>, grantResults: IntArray) {
        super.onRequestPermissionsResult(requestCode, permissions, grantResults)
        if (requestCode == REQ_RECORD_AUDIO && grantResults.firstOrNull() == PackageManager.PERMISSION_GRANTED) {
            MemoryCaptureService.enableAndStart(this)
        }
        refreshCaptureControl()
    }

    override fun onBackPressed() {
        if (webView.canGoBack()) {
            webView.goBack()
        } else {
            super.onBackPressed()
        }
    }

    override fun onDestroy() {
        webView.destroy()
        super.onDestroy()
    }
}
