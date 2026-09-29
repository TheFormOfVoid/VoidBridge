package com.theformofvoid.voidbridge

import android.Manifest
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.ClipData
import android.content.ClipDescription
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
import android.os.PersistableBundle
import android.provider.Settings
import android.util.Log
import com.theformofvoid.voidbridge.core.Content
import com.theformofvoid.voidbridge.core.Device
import com.theformofvoid.voidbridge.core.FileMeta
import com.theformofvoid.voidbridge.core.Identity
import com.theformofvoid.voidbridge.core.Message
import com.theformofvoid.voidbridge.core.Node
import com.theformofvoid.voidbridge.core.PeerManager
import com.theformofvoid.voidbridge.core.Protocol
import com.theformofvoid.voidbridge.core.RelayClient
import java.io.File
import java.io.InputStream
import java.util.concurrent.Executors

/**
 * Foreground service that runs sync: direct links to devices on the same
 * network or Tailscale, and/or the connection to a VoidBridge server.
 */
class SyncService : Service(), Node.Listener {
    private val main = Handler(Looper.getMainLooper())
    private lateinit var prefs: Prefs
    private lateinit var clipboard: ClipboardManager
    private var node: Node? = null
    private var peers: PeerManager? = null
    private var relay: RelayClient? = null
    private var logcat: LogcatWatcher? = null
    private var multicastLock: WifiManager.MulticastLock? = null
    private var wifiLock: WifiManager.WifiLock? = null
    private var lastReaderLaunch = 0L
    @Volatile private var serverUp = false
    @Volatile private var serverErr: String? = null
    @Volatile private var peerErr: String? = null
    private var updatePosted = false
    private lateinit var notes: TransferNotes
    private val updateTimer = Executors.newSingleThreadScheduledExecutor { r -> Thread(r, "voidbridge-updates").apply { isDaemon = true } }
    private val sendQueue = Executors.newSingleThreadExecutor { r -> Thread(r, "voidbridge-files-out").apply { isDaemon = true } }

    private val networkCallback = object : ConnectivityManager.NetworkCallback() {
        override fun onAvailable(network: Network) {
            peers?.kick()
            relay?.kick()
        }
    }

    private val clipListener = ClipboardManager.OnPrimaryClipChangedListener {
        // Delivered only when we may read: Android 9 and older, or while one
        // of our activities has focus.
        Thread { readClipboard()?.let { node?.localCopy(it) } }.start()
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        instance = this
        prefs = Prefs(this)
        clipboard = getSystemService(ClipboardManager::class.java)
        createChannel()
        notes = TransferNotes(this)
        if (AppUpdater.supported(this)) {
            updateTimer.scheduleWithFixedDelay({ AppUpdater.check(this) }, 1, 6 * 60, java.util.concurrent.TimeUnit.MINUTES)
        }
        goForeground(getString(R.string.status_starting))
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (!prefs.configured) {
            stopSelf()
            return START_NOT_STICKY
        }
        if (node == null) startSync()
        return START_STICKY
    }

    private fun deviceName(): String =
        Settings.Global.getString(contentResolver, Settings.Global.DEVICE_NAME) ?: Build.MODEL

    private fun startSync() {
        val keys = prefs.keys ?: return
        val n = Node(Identity(prefs.deviceId, deviceName(), "android"), keys, this)
        n.paused = prefs.paused
        n.skipSensitive = prefs.skipSensitive
        n.fileReceiver = ReceivedFiles(this, prefs, notes)
        node = n

        val wifi = applicationContext.getSystemService(WifiManager::class.java)
        @Suppress("DEPRECATION")
        wifiLock = wifi.createWifiLock(WifiManager.WIFI_MODE_FULL_HIGH_PERF, "voidbridge").apply { setReferenceCounted(false); acquire() }

        if (prefs.direct) {
            multicastLock = wifi.createMulticastLock("voidbridge").apply { setReferenceCounted(false); acquire() }
            val pm = PeerManager(n, keys, log = { Log.d(TAG, it) })
            try {
                pm.setManual(prefs.manualList)
                pm.start()
                peers = pm
            } catch (e: Exception) {
                peerErr = getString(R.string.err_port, e.message ?: "")
                Log.w(TAG, "direct links: $e")
            }
        }
        if (prefs.mode == MODE_ACCOUNT && prefs.token.isNotEmpty()) {
            relay = RelayClient(prefs.server, prefs.token, n, object : RelayClient.Listener {
                override fun onState(connected: Boolean, error: String?) {
                    serverUp = connected
                    serverErr = error
                    changed()
                }

                override fun onUnauthorized() {
                    serverErr = getString(R.string.err_signed_out)
                    changed()
                }
            }).also { it.start() }
        }

        getSystemService(ConnectivityManager::class.java).registerDefaultNetworkCallback(networkCallback)
        clipboard.addPrimaryClipChangedListener(clipListener)
        startLogcatWatcher()
        changed()
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

    /** Called with content read by ClipboardReaderActivity, ShareActivity or the tile. */
    fun onLocalContent(c: Content, manual: Boolean = false) {
        val n = node ?: return
        Thread {
            if (manual) n.forgetLastHash() // an explicit "send" always sends
            n.localCopy(c)
        }.start()
    }

    /** Reads the clipboard (text or image). Only works while we may read it. */
    fun readClipboard(): Content? = readClip(this, clipboard)

    fun setPaused(p: Boolean) {
        prefs.paused = p
        node?.paused = p
        changed()
    }

    fun setSkipSensitive(s: Boolean) {
        prefs.skipSensitive = s
        node?.skipSensitive = s
    }

    fun setManual(list: List<String>) = peers?.setManual(list)

    /**
     * Sends files to a device, one after another, with notifications. If the
     * device isn't connected yet it waits a little for it.
     */
    fun sendFiles(device: String, deviceName: String, items: List<Outgoing>) {
        sendQueue.execute {
            var ready = false
            for (i in 0 until 40) { // up to 20 s
                if (node?.devices()?.any { it.id == device } == true) {
                    ready = true
                    break
                }
                Thread.sleep(500)
            }
            for (item in items) {
                val key = "out-" + Protocol.newId()
                var temp: File? = null
                try {
                    val n = node
                    if (!ready || n == null) throw java.io.IOException(getString(R.string.not_connected, deviceName))
                    var size = item.size
                    var input: InputStream = android.os.ParcelFileDescriptor.AutoCloseInputStream(item.pfd)
                    if (size < 0) { // a stream of unknown length: copy it first
                        val t = File.createTempFile("send-", ".tmp", cacheDir).also { temp = it }
                        input.use { src -> t.outputStream().use { src.copyTo(it) } }
                        size = t.length()
                        input = t.inputStream()
                    }
                    val title = getString(R.string.sending_file, item.name, deviceName)
                    notes.progress(key, title, 0, size)
                    var last = 0L
                    input.use {
                        n.sendFile(device, FileMeta(item.name, size, item.mime), it, { sent ->
                            val now = System.currentTimeMillis()
                            if (now - last > 500) {
                                last = now
                                notes.progress(key, title, sent, size)
                            }
                        })
                    }
                    notes.done(key, getString(R.string.sent_file, item.name, deviceName), null, null)
                } catch (e: Exception) {
                    Log.w(TAG, "sending ${item.name}: $e")
                    notes.done(key, getString(R.string.send_failed, item.name, deviceName), e.message, null)
                } finally {
                    try { item.pfd.close() } catch (_: Exception) {}
                    temp?.delete()
                }
            }
        }
    }

    // ---- Node.Listener ----

    override fun apply(content: Content, from: Message) {
        logcat?.suppressUntil = System.currentTimeMillis() + 1_500
        val clip = when (content.type) {
            Protocol.CLIP_TEXT -> ClipData.newPlainText("VoidBridge", content.text)
            else -> {
                val uri = try {
                    ClipProvider.save(this, from.id, content.data, content.mime)
                } catch (e: Exception) {
                    Log.w(TAG, "saving image: $e")
                    return
                }
                ClipData.newUri(contentResolver, "Image from ${from.originName}", uri)
            }
        }
        main.post {
            try {
                clipboard.setPrimaryClip(clip)
            } catch (e: Exception) {
                Log.w(TAG, "setPrimaryClip: $e")
            }
        }
    }

    override fun changed() {
        main.post {
            if (updatePosted) return@post
            updatePosted = true
            main.postDelayed({
                updatePosted = false
                val st = status()
                lastStatus = st
                Shortcuts.update(this, prefs, st.devices)
                goForeground(st.text)
                statusListener?.invoke(st)
            }, 200)
        }
    }

    override fun log(msg: String) {
        Log.d(TAG, msg)
    }

    /** What the UI shows. */
    data class Status(val text: String, val connected: Boolean, val devices: List<Device>, val serverUp: Boolean, val serverErr: String?, val peerErr: String?, val paused: Boolean)

    fun status(): Status {
        val devices = node?.devices() ?: emptyList()
        val text = when {
            prefs.paused -> getString(R.string.status_paused)
            devices.size == 1 -> getString(R.string.status_one, devices[0].name)
            devices.size > 1 -> getString(R.string.status_many, devices.size)
            prefs.mode == MODE_ACCOUNT && !serverUp -> getString(R.string.status_server_down)
            else -> getString(R.string.status_waiting)
        }
        return Status(text, devices.isNotEmpty(), devices, serverUp, serverErr, peerErr, prefs.paused)
    }

    override fun onDestroy() {
        instance = null
        relay?.stop()
        peers?.stop()
        node?.shutdown()
        sendQueue.shutdownNow()
        updateTimer.shutdownNow()
        logcat?.stop()
        clipboard.removePrimaryClipChangedListener(clipListener)
        try {
            getSystemService(ConnectivityManager::class.java).unregisterNetworkCallback(networkCallback)
        } catch (_: Exception) {
        }
        multicastLock?.release()
        wifiLock?.release()
        lastStatus = null
        statusListener?.invoke(null)
        super.onDestroy()
    }

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
        const val MODE_CODE = "code"
        const val MODE_ACCOUNT = "account"

        @Volatile var instance: SyncService? = null
            private set

        var lastStatus: Status? = null
        val lastConnected get() = lastStatus?.connected == true
        var statusListener: ((Status?) -> Unit)? = null

        fun hasReadLogs(ctx: Context) =
            ctx.checkSelfPermission(Manifest.permission.READ_LOGS) == PackageManager.PERMISSION_GRANTED

        fun start(ctx: Context) {
            ctx.startForegroundService(Intent(ctx, SyncService::class.java))
        }

        fun stop(ctx: Context) {
            ctx.stopService(Intent(ctx, SyncService::class.java))
        }

        private const val EXTRA_IS_SENSITIVE = "android.content.extra.IS_SENSITIVE"

        /** Reads text or an image from the clipboard; null if nothing usable or not allowed. */
        fun readClip(ctx: Context, cm: ClipboardManager): Content? {
            val clip = try {
                cm.primaryClip
            } catch (e: SecurityException) {
                null
            } ?: return null
            if (clip.itemCount == 0) return null
            val desc: ClipDescription = clip.description
            val extras: PersistableBundle? = if (Build.VERSION.SDK_INT >= 24) desc.extras else null
            val sensitive = extras?.getBoolean(EXTRA_IS_SENSITIVE, false) == true
            val item = clip.getItemAt(0)
            val uri = item.uri
            if (uri != null) {
                val mime = ctx.contentResolver.getType(uri) ?: (0 until desc.mimeTypeCount).map { desc.getMimeType(it) }.firstOrNull { it.startsWith("image/") }
                if (mime != null && mime.startsWith("image/")) {
                    return try {
                        ctx.contentResolver.openInputStream(uri)?.use { input ->
                            val buf = input.readNBytesCompat(Protocol.MAX_IMAGE + 1)
                            if (buf.size > Protocol.MAX_IMAGE) null else Content(Protocol.CLIP_IMAGE, data = buf, mime = mime, sensitive = sensitive)
                        }
                    } catch (e: Exception) {
                        Log.w(TAG, "reading clipboard image: $e")
                        null
                    }
                }
            }
            val text = item.coerceToText(ctx)?.toString()
            if (text.isNullOrEmpty()) return null
            return Content(Protocol.CLIP_TEXT, text = text, sensitive = sensitive)
        }

        private fun java.io.InputStream.readNBytesCompat(max: Int): ByteArray {
            val out = java.io.ByteArrayOutputStream()
            val buf = ByteArray(64 * 1024)
            while (out.size() <= max) {
                val n = read(buf)
                if (n < 0) break
                out.write(buf, 0, n)
            }
            return out.toByteArray()
        }
    }
}
