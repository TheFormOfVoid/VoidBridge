package com.theformofvoid.voidbridge

import android.app.Activity
import android.content.Intent
import android.os.Bundle
import android.widget.Toast

/** "Share → VoidBridge": sends shared text to the PC clipboard. */
class ShareActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val text = intent.getStringExtra(Intent.EXTRA_TEXT)
        val service = SyncService.instance
        when {
            text.isNullOrEmpty() -> {}
            service == null -> Toast.makeText(this, R.string.not_running, Toast.LENGTH_SHORT).show()
            else -> {
                service.onLocalText(text)
                Toast.makeText(this, R.string.sent, Toast.LENGTH_SHORT).show()
            }
        }
        finish()
    }
}
