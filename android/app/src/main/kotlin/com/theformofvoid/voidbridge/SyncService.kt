package com.theformofvoid.voidbridge

import android.Manifest
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.content.pm.ServiceInfo
import android.net.ConnectivityManager
import android.net.Network
import android.net.wifi.WifiManager
import android.os.Build
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.provider.Settings
import android.util.Log
import com.theformofvoid.voidbridge.core.Discovery
import com.theformofvoid.voidbridge.core.HostSource
import com.theformofvoid.voidbridge.core.Protocol
import com.theformofvoid.voidbridge.core.SyncClient
import java.net.InetSocketAddress

/**
 * Foreground service that keeps the connection to the PC alive and moves
 * clipboard contents both ways.
 */
class SyncService : Service(), SyncClient.Listener {
    private val main = Handler(Looper.getMainLooper())
    private lateinit var prefs: Prefs
    private lateinit var clipboard: ClipboardManager
    private var client: SyncClient? = null
    private var discovery: Discovery? = null
    private var logcat: LogcatWatcher? = null
    private var multicastLock: WifiManager.MulticastLock? = null
    private var wifiLock: WifiManager.WifiLock? = null
    private var lastReaderLaunch = 0L

    private val networkCallback = object : ConnectivityManager.NetworkCallback() {
        override fun onAvailable(network: Network) {
            client?.kick()
        }

        override fun onLost(network: Network) {
            client?.reconnect()
        }
    }

    private val clipListener = ClipboardManager.OnPrimaryClipChangedListener {
        // Delivered only when we're allowed to read: Android 9 and older, or
        // while one of our activities has focus.
        readClipboard()?.let { client?.localClip(it) }
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        instance = this
        prefs = Prefs(this)
        clipboard = getSystemService(ClipboardManager::class.java)
        createChannel()
        goForeground(getString(R.string.status_starting))
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val key = prefs.pairingKey
        if (key == null) {
            stopSelf()
            return START_NOT_STICKY
        }
        if (client == null) startSync(key)
        return START_STICKY
    }

    private fun startSync(key: ByteArray) {
        val wifi = applicationContext.getSystemService(WifiManager::class.java)
        multicastLock = wifi.createMulticastLock("voidbridge-discovery").apply { setReferenceCounted(false) }
        @Suppress("DEPRECATION")
        wifiLock = wifi.createWifiLock(WifiManager.WIFI_MODE_FULL_HIGH_PERF, "voidbridge").apply {
            setReferenceCounted(false)
            acquire()
        }

        val disc = Discovery(Protocol.fingerprint(key)) { Log.d(TAG, it) }
        discovery = disc
        multicastLock?.acquire()
        disc.start()

        val hosts = HostSource {
            val out = LinkedHashSet<InetSocketAddress>()
            Prefs.parseHost(prefs.manualHost)?.let(out::add)
            var found = disc.recent()
            if (found.isEmpty()) {
                disc.probe()
                Thread.sleep(1_000)
                found = disc.recent()
            }
            out.addAll(found)
            Prefs.parseHost(prefs.lastHost)?.let(out::add)
            out.toList()
        }
        val c = SyncClient(key, prefs.deviceId, deviceName(), hosts, this)
        client = c
        c.start()

        getSystemService(ConnectivityManager::class.java).registerDefaultNetworkCallback(networkCallback)
        clipboard.addPrimaryClipChangedListener(clipListener)
        startLogcatWatcher()
    }

    /** Starts (or restarts) background clipboard detection if we have READ_LOGS. */
    fun startLogcatWatcher(restart: Boolean = false) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.Q || !hasReadLogs(this)) return
        val w = logcat ?: LogcatWatcher(packageName) { onBackgroundClipChange() }.also { logcat = it }
        w.start()
        if (restart) w.restart()
    }

    private fun onBackgroundClipChange() {
        val now = System.currentTimeMillis()
        if (now - lastReaderLaunch < 300) return
        lastReaderLaunch = now
        if (!Settings.canDrawOverlays(this)) {
            Log.w(TAG, "clipboard changed but 'display over other apps' is not allowed")
            return
        }
        main.post { ClipboardReaderActivity.launch(this) }
    }

    /** Called with text read by ClipboardReaderActivity, ShareActivity or the tile. */
    fun onLocalText(text: String) {
        client?.localClip(text)
    }

    fun readClipboard(): String? = try {
        clipboard.primaryClip?.takeIf { it.itemCount > 0 }?.getItemAt(0)?.coerceToText(this)?.toString()
    } catch (e: SecurityException) {
        null
    }

    // SyncClient.Listener

    override fun applyRemoteClip(text: String) {
        logcat?.suppressUntil = System.currentTimeMillis() + 1_500
        main.post {
            try {
                clipboard.setPrimaryClip(ClipData.newPlainText("VoidBridge", text))
            } catch (e: Exception) {
                Log.w(TAG, "setPrimaryClip: $e")
            }
        }
    }

    override fun onStatus(status: SyncClient.Status) {
        val connected = status is SyncClient.Status.Connected
        if (connected) multicastLock?.release() else multicastLock?.acquire()
        val text = when (status) {
            is SyncClient.Status.Searching -> getString(R.string.status_searching)
            is SyncClient.Status.Connecting -> getString(R.string.status_connecting, status.address)
            is SyncClient.Status.Connected -> getString(R.string.status_connected, status.pcName)
            is SyncClient.Status.Failed -> status.reason
        }
        main.post {
            lastStatus = text
            lastConnected = connected
            goForeground(text)
            statusListener?.invoke(text, connected)
        }
    }

    override fun onConnectedTo(address: InetSocketAddress) {
        prefs.lastHost = "${address.hostString}:${address.port}"
    }

    override fun log(msg: String) {
        Log.d(TAG, msg)
    }

    override fun onDestroy() {
        instance = null
        client?.stop()
        discovery?.stop()
        logcat?.stop()
        clipboard.removePrimaryClipChangedListener(clipListener)
        try {
            getSystemService(ConnectivityManager::class.java).unregisterNetworkCallback(networkCallback)
        } catch (_: Exception) {
        }
        multicastLock?.release()
        wifiLock?.release()
        lastStatus = getString(R.string.status_stopped)
        lastConnected = false
        statusListener?.invoke(lastStatus, false)
        super.onDestroy()
    }

    private fun deviceName(): String =
        Settings.Global.getString(contentResolver, Settings.Global.DEVICE_NAME) ?: Build.MODEL

    private fun createChannel() {
        val ch = NotificationChannel(CHANNEL, getString(R.string.channel_name), NotificationManager.IMPORTANCE_LOW)
        ch.setShowBadge(false)
        getSystemService(NotificationManager::class.java).createNotificationChannel(ch)
    }

    private fun goForeground(text: String) {
        val open = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
        val send = PendingIntent.getActivity(this, 1, ClipboardReaderActivity.intent(this), PendingIntent.FLAG_IMMUTABLE)
        val n = Notification.Builder(this, CHANNEL)
            .setSmallIcon(R.drawable.ic_stat)
            .setContentTitle(getString(R.string.app_name))
            .setContentText(text)
            .setContentIntent(open)
            .setOngoing(true)
            .addAction(Notification.Action.Builder(null, getString(R.string.action_send), send).build())
            .build()
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            startForeground(NOTIFICATION_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_CONNECTED_DEVICE)
        } else {
            startForeground(NOTIFICATION_ID, n)
        }
    }

    companion object {
        private const val TAG = "VoidBridge"
        private const val CHANNEL = "sync"
        private const val NOTIFICATION_ID = 1

        @Volatile var instance: SyncService? = null
            private set

        var lastStatus = ""
        var lastConnected = false
        var statusListener: ((String, Boolean) -> Unit)? = null

        fun hasReadLogs(ctx: Context) =
            ctx.checkSelfPermission(Manifest.permission.READ_LOGS) == PackageManager.PERMISSION_GRANTED

        fun start(ctx: Context) {
            ctx.startForegroundService(Intent(ctx, SyncService::class.java))
        }

        fun stop(ctx: Context) {
            Prefs(ctx).enabled = false
            ctx.stopService(Intent(ctx, SyncService::class.java))
        }
    }
}
