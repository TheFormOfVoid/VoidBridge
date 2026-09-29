package com.theformofvoid.voidbridge

import android.content.Context
import android.content.Intent
import android.content.pm.ShortcutInfo
import android.content.pm.ShortcutManager
import android.graphics.drawable.Icon
import android.os.Build
import android.util.Log
import com.theformofvoid.voidbridge.core.Device

/**
 * Your devices as sharing shortcuts, so they show up directly in Android's
 * share sheet (and when long-pressing the app icon).
 */
object Shortcuts {
    const val CATEGORY = "com.theformofvoid.voidbridge.category.SEND_TO_DEVICE"
    const val ACTION_SEND_TO = "com.theformofvoid.voidbridge.SEND_TO"
    const val EXTRA_DEVICE = "device"
    private const val PREFIX = "device:"

    fun deviceFor(shortcutId: String?): String? = shortcutId?.takeIf { it.startsWith(PREFIX) }?.removePrefix(PREFIX)

    /** Remembers connected devices and republishes the shortcuts if the list changed. */
    fun update(ctx: Context, prefs: Prefs, connected: List<Device>) {
        if (!prefs.remember(connected)) return
        publish(ctx, prefs)
    }

    fun publish(ctx: Context, prefs: Prefs) {
        val sm = ctx.getSystemService(ShortcutManager::class.java) ?: return
        val list = prefs.knownDevices().sortedByDescending { it.seen }.take(sm.maxShortcutCountPerActivity.coerceAtMost(4)).mapIndexed { i, d ->
            ShortcutInfo.Builder(ctx, PREFIX + d.id)
                .setShortLabel(d.name)
                .setLongLabel(ctx.getString(R.string.send_to, d.name))
                .setIcon(Icon.createWithResource(ctx, R.mipmap.ic_launcher))
                .setCategories(setOf(CATEGORY))
                .setRank(i)
                .setIntent(Intent(ctx, MainActivity::class.java).setAction(ACTION_SEND_TO).putExtra(EXTRA_DEVICE, d.id))
                .apply { if (Build.VERSION.SDK_INT >= 30) setLongLived(true) }
                .build()
        }
        try {
            sm.dynamicShortcuts = list
        } catch (e: Exception) { // rate limited in the background; retried on the next change
            Log.w("VoidBridge", "shortcuts: $e")
        }
    }
}
