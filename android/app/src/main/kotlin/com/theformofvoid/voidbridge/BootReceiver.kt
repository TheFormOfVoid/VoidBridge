package com.theformofvoid.voidbridge

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

/** Restarts syncing after a reboot or an app update. */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val prefs = Prefs(context)
        if (prefs.enabled && prefs.configured) {
            try {
                SyncService.start(context)
            } catch (_: Exception) {
                // Some OEMs refuse background starts even here; opening the app fixes it.
            }
        }
    }
}
