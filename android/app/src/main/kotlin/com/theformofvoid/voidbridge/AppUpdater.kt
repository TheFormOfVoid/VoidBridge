package com.theformofvoid.voidbridge

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInstaller
import android.net.Uri
import android.os.Build
import android.provider.Settings
import android.util.Log
import com.theformofvoid.voidbridge.core.Updates
import java.io.File

/**
 * Checks GitHub for new stable releases. With automatic updates on, it
 * downloads the APK, verifies it and hands it to Android's installer, which
 * asks for one tap (Android also checks the new APK has the same signing key).
 * With them off, it only notifies that an update exists.
 */
object AppUpdater {
    private const val TAG = "VoidBridge"
    private const val CHANNEL = "updates"
    private const val NOTIFICATION_ID = 7
    private val lock = Any()

    fun currentVersion(ctx: Context): String = try {
        ctx.packageManager.getPackageInfo(ctx.packageName, 0).versionName ?: ""
    } catch (_: Exception) {
        ""
    }

    fun supported(ctx: Context) = Updates.supported(currentVersion(ctx))

    fun canInstall(ctx: Context) = ctx.packageManager.canRequestPackageInstalls()

    /** Opens the system page where the user can let VoidBridge install updates. */
    fun allowInstallsIntent(ctx: Context) =
        Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:${ctx.packageName}"))

    /**
     * Checks for an update (blocking). [force] downloads and installs it
     * even with automatic updates off. Returns a message for the user.
     */
    fun check(ctx: Context, force: Boolean = false): String = synchronized(lock) {
        val prefs = Prefs(ctx)
        val current = currentVersion(ctx)
        if (!Updates.supported(current)) return ctx.getString(R.string.update_dev_build)
        val rel = try {
            Updates.latest()
        } catch (e: Exception) {
            Log.w(TAG, "update check: $e")
            return e.message ?: e.toString()
        }
        if (!Updates.newer(rel.version, current)) {
            prefs.availableUpdate = ""
            cancel(ctx)
            return ctx.getString(R.string.update_latest, current)
        }
        prefs.availableUpdate = rel.version
        if (!force && !prefs.autoUpdate) {
            notify(ctx, ctx.getString(R.string.update_available, rel.version), ctx.getString(R.string.update_open_app), openApp(ctx))
            return ctx.getString(R.string.update_available, rel.version)
        }
        if (!canInstall(ctx)) {
            val pi = PendingIntent.getActivity(ctx, 3, allowInstallsIntent(ctx).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK), PendingIntent.FLAG_IMMUTABLE)
            notify(ctx, ctx.getString(R.string.update_available, rel.version), ctx.getString(R.string.update_need_permission), pi)
            return ctx.getString(R.string.update_need_permission)
        }
        val apk = File(ctx.cacheDir, "update.apk")
        try {
            apk.outputStream().use { Updates.fetch(rel, Updates.APK, it) }
            install(ctx, apk)
        } catch (e: Exception) {
            Log.w(TAG, "update: $e")
            apk.delete()
            notify(ctx, ctx.getString(R.string.update_failed, rel.version), e.message, openApp(ctx))
            return e.message ?: e.toString()
        }
        return ctx.getString(R.string.update_installing, rel.version)
    }

    private fun install(ctx: Context, apk: File) {
        val pi = ctx.packageManager.packageInstaller
        val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL).apply {
            setAppPackageName(ctx.packageName)
            // Skips the confirmation when Android allows it (Android 12+, and
            // only once VoidBridge installed the current version itself).
            if (Build.VERSION.SDK_INT >= 31) setRequireUserAction(PackageInstaller.SessionParams.USER_ACTION_NOT_REQUIRED)
        }
        val id = pi.createSession(params)
        pi.openSession(id).use { s ->
            s.openWrite("VoidBridge.apk", 0, apk.length()).use { out ->
                apk.inputStream().use { it.copyTo(out) }
                s.fsync(out)
            }
            val flags = PendingIntent.FLAG_UPDATE_CURRENT or (if (Build.VERSION.SDK_INT >= 31) PendingIntent.FLAG_MUTABLE else 0)
            val result = PendingIntent.getBroadcast(ctx, 0, Intent(ctx, UpdateReceiver::class.java), flags)
            s.commit(result.intentSender)
        }
        apk.delete()
    }

    private fun openApp(ctx: Context) =
        PendingIntent.getActivity(ctx, 2, Intent(ctx, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)

    fun notify(ctx: Context, title: String, text: String?, tap: PendingIntent?) {
        val nm = ctx.getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(NotificationChannel(CHANNEL, ctx.getString(R.string.channel_updates), NotificationManager.IMPORTANCE_DEFAULT))
        val n = Notification.Builder(ctx, CHANNEL)
            .setSmallIcon(R.drawable.ic_stat)
            .setContentTitle(title)
            .setAutoCancel(true)
            .apply {
                if (text != null) setContentText(text).setStyle(Notification.BigTextStyle().bigText(text))
                if (tap != null) setContentIntent(tap)
            }
            .build()
        try { nm.notify(NOTIFICATION_ID, n) } catch (_: SecurityException) {}
    }

    fun cancel(ctx: Context) = ctx.getSystemService(NotificationManager::class.java).cancel(NOTIFICATION_ID)
}

/** Receives the result of an update install. */
class UpdateReceiver : BroadcastReceiver() {
    override fun onReceive(ctx: Context, intent: Intent) {
        when (intent.getIntExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_FAILURE)) {
            PackageInstaller.STATUS_PENDING_USER_ACTION -> {
                @Suppress("DEPRECATION")
                val confirm = (if (Build.VERSION.SDK_INT >= 33) intent.getParcelableExtra(Intent.EXTRA_INTENT, Intent::class.java) else intent.getParcelableExtra(Intent.EXTRA_INTENT)) ?: return
                confirm.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
                if (MainActivity.visible) {
                    ctx.startActivity(confirm)
                } else {
                    val pi = PendingIntent.getActivity(ctx, 4, confirm, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
                    val v = Prefs(ctx).availableUpdate
                    AppUpdater.notify(ctx, ctx.getString(R.string.update_ready, v), ctx.getString(R.string.update_tap_install), pi)
                }
            }
            PackageInstaller.STATUS_SUCCESS -> Unit // we're replaced; BootReceiver restarts sync
            else -> {
                val msg = intent.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE)
                if (intent.getIntExtra(PackageInstaller.EXTRA_STATUS, 0) != PackageInstaller.STATUS_FAILURE_ABORTED) {
                    AppUpdater.notify(ctx, ctx.getString(R.string.update_failed, Prefs(ctx).availableUpdate), msg, null)
                }
            }
        }
    }
}
