package com.goldenfinger.butler

import android.content.Intent
import android.net.Uri
import android.os.Handler
import android.os.Looper
import android.webkit.JavascriptInterface
import android.widget.Toast
import androidx.core.content.FileProvider
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import java.io.File
import java.security.MessageDigest

/**
 * 交付物原生下载桥：WebView 只负责展示列表，实际下载/校验/保存/分享在 Kotlin 完成。
 * 文件保存到 App 私有目录 files/deliveries，校验 SHA-256 后才 ACK 云端。
 */
class DeliveryBridge(private val activity: MainActivity) {
    private val client = OkHttpClient()
    private val main = Handler(Looper.getMainLooper())

    @JavascriptInterface
    fun save(deliveryId: String, name: String, sha256: String) {
        Thread {
            try {
                val base = GoBridge.baseUrl() ?: throw IllegalStateException("服务未就绪")
                val token = GoBridge.token() ?: throw IllegalStateException("缺少访问令牌")
                val url = "http://$base/api/delivery/${Uri.encode(deliveryId)}/content"
                client.newCall(
                    Request.Builder().url(url).header("X-Token", token).get().build()
                ).execute().use { response ->
                    if (!response.isSuccessful) throw IllegalStateException("HTTP ${response.code}")
                    val body = response.body ?: throw IllegalStateException("空响应")
                    val dir = File(activity.filesDir, "deliveries").apply { mkdirs() }
                    val file = File(dir, safeName(name))
                    body.byteStream().use { input ->
                        file.outputStream().use { output -> input.copyTo(output) }
                    }
                    val actual = sha256Hex(file)
                    if (sha256.isNotBlank() && !actual.equals(sha256, ignoreCase = true)) {
                        file.delete()
                        throw IllegalStateException("SHA-256 校验失败")
                    }
                    ack(base, token, deliveryId)
                    main.post { share(file, name) }
                }
            } catch (t: Throwable) {
                main.post {
                    Toast.makeText(activity, "下载失败：${t.message}", Toast.LENGTH_LONG).show()
                }
            }
        }.start()
    }

    private fun ack(base: String, token: String, deliveryId: String) {
        try {
            val url = "http://$base/api/delivery/${Uri.encode(deliveryId)}/ack"
            client.newCall(
                Request.Builder().url(url).header("X-Token", token)
                    .post(ByteArray(0).toRequestBody("application/json".toMediaType()))
                    .build()
            ).execute().close()
        } catch (_: Throwable) {
            // ACK 失败不删除已校验文件；下次打开会重新显示未接收。
        }
    }

    private fun share(file: File, name: String) {
        val uri = FileProvider.getUriForFile(activity, activity.packageName + ".files", file)
        val mime = activity.contentResolver.getType(uri) ?: "application/octet-stream"
        val send = Intent(Intent.ACTION_SEND).apply {
            type = mime
            putExtra(Intent.EXTRA_STREAM, uri)
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        }
        val open = Intent(Intent.ACTION_VIEW).apply {
            setDataAndType(uri, mime)
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK)
        }
        val chooser = Intent.createChooser(send, "保存或分享 $name").apply {
            putExtra(Intent.EXTRA_INITIAL_INTENTS, arrayOf(open))
            addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        }
        activity.startActivity(chooser)
    }

    private fun safeName(name: String): String {
        val base = name.substringAfterLast('/').substringAfterLast('\\').trim()
        val cleaned = base.filter { it >= ' ' && it != '/' && it != '\\' }
        return if (cleaned.isBlank()) "download" else cleaned.take(120)
    }

    private fun sha256Hex(file: File): String {
        val digest = MessageDigest.getInstance("SHA-256")
        file.inputStream().use { input ->
            val buffer = ByteArray(64 * 1024)
            while (true) {
                val read = input.read(buffer)
                if (read <= 0) break
                digest.update(buffer, 0, read)
            }
        }
        return digest.digest().joinToString("") { "%02x".format(it) }
    }
}
