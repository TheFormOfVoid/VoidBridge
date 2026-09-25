package com.theformofvoid.voidbridge

import android.app.Activity
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.widget.Toast
import com.theformofvoid.voidbridge.core.Content
import com.theformofvoid.voidbridge.core.Protocol

/** "Share → VoidBridge": sends shared text or an image to all your devices' clipboards. */
class ShareActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val service = SyncService.instance
        val content = read()
        when {
            content == null -> Toast.makeText(this, R.string.nothing_to_send, Toast.LENGTH_SHORT).show()
            service == null -> Toast.makeText(this, R.string.not_running, Toast.LENGTH_SHORT).show()
            else -> {
                service.onLocalContent(content, manual = true)
                Toast.makeText(this, R.string.sent, Toast.LENGTH_SHORT).show()
            }
        }
        finish()
    }

    private fun read(): Content? {
        val type = intent.type ?: return null
        if (type.startsWith("image/")) {
            val uri: Uri = (if (Build.VERSION.SDK_INT >= 33) intent.getParcelableExtra(Intent.EXTRA_STREAM, Uri::class.java)
            else @Suppress("DEPRECATION") intent.getParcelableExtra(Intent.EXTRA_STREAM)) ?: return null
            return try {
                contentResolver.openInputStream(uri)?.use { s ->
                    val data = s.readBytes()
                    if (data.size > Protocol.MAX_IMAGE) null else Content(Protocol.CLIP_IMAGE, data = data, mime = contentResolver.getType(uri) ?: type)
                }
            } catch (e: Exception) {
                null
            }
        }
        val text = intent.getStringExtra(Intent.EXTRA_TEXT) ?: return null
        return Content(Protocol.CLIP_TEXT, text = text)
    }
}
