package com.theformofvoid.voidbridge

import android.app.Activity
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.os.Bundle
import android.widget.Toast

/**
 * An invisible activity that exists only to gain window focus for a moment,
 * because Android lets only the focused app read the clipboard. It reads the
 * clip (text or image), hands it to SyncService and finishes immediately.
 */
class ClipboardReaderActivity : Activity() {
    private var done = false

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        @Suppress("DEPRECATION")
        overridePendingTransition(0, 0)
    }

    override fun onWindowFocusChanged(hasFocus: Boolean) {
        super.onWindowFocusChanged(hasFocus)
        if (!hasFocus || done) return
        done = true
        val manual = intent.getBooleanExtra(EXTRA_MANUAL, false)
        val content = SyncService.readClip(this, getSystemService(ClipboardManager::class.java))
        val service = SyncService.instance
        when {
            content == null -> if (manual) Toast.makeText(this, R.string.nothing_to_send, Toast.LENGTH_SHORT).show()
            service == null -> if (manual) Toast.makeText(this, R.string.not_running, Toast.LENGTH_SHORT).show()
            else -> {
                service.onLocalContent(content, manual)
                if (manual) Toast.makeText(this, R.string.sent, Toast.LENGTH_SHORT).show()
            }
        }
        finish()
        @Suppress("DEPRECATION")
        overridePendingTransition(0, 0)
    }

    companion object {
        private const val EXTRA_MANUAL = "manual"

        fun intent(ctx: Context, manual: Boolean = true): Intent =
            Intent(ctx, ClipboardReaderActivity::class.java)
                .putExtra(EXTRA_MANUAL, manual)
                .addFlags(
                    Intent.FLAG_ACTIVITY_NEW_TASK or Intent.FLAG_ACTIVITY_NO_ANIMATION or
                        Intent.FLAG_ACTIVITY_EXCLUDE_FROM_RECENTS or Intent.FLAG_ACTIVITY_NO_HISTORY,
                )

        /** Launch from the background (requires "display over other apps"). */
        fun launch(ctx: Context) {
            try {
                ctx.startActivity(intent(ctx, manual = false))
            } catch (_: Exception) {
            }
        }
    }
}
