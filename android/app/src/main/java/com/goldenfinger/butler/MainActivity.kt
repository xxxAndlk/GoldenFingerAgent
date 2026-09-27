package com.goldenfinger.butler

import android.annotation.SuppressLint
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.provider.Settings
import android.view.KeyEvent
import android.view.ViewGroup
import android.webkit.WebResourceRequest
import android.webkit.WebResourceResponse
import android.webkit.WebSettings
import android.webkit.WebView
import android.webkit.WebViewClient
import android.widget.Toast
import androidx.appcompat.app.AppCompatActivity

/**
 * MainActivity：全屏 WebView 壳。
 * URL = http://127.0.0.1:PORT/?token=..&user_id=..（Web 页 apiFetch 读取 query
 * token/user_id 并注入 X-Token / X-User-Id，无需改服务端）。
 * JS/DOM storage 开启；settings/config/SQLite 均在 App 私有目录。
 */
class MainActivity : AppCompatActivity() {
    private lateinit var webView: WebView

    @SuppressLint("SetJavaScriptEnabled")
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        // 确保服务与 Go 服务在跑（冷启动路径）
        GoForegroundService.start(this)
        KeepAliveWorker.schedule(this)

        webView = WebView(this)
        webView.layoutParams = ViewGroup.LayoutParams(
            ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.MATCH_PARENT
        )
        setContentView(webView)

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

    private fun loadUrl(addr: String, token: String, uid: String) {
        val url = if (uid.isBlank()) "http://$addr/?token=$token"
        else "http://$addr/?token=$token&user_id=$uid"
        webView.loadUrl(url)
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
