package com.theformofvoid.voidbridge

import android.app.Activity
import android.app.AlertDialog
import android.content.Intent
import android.content.res.Configuration
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.provider.OpenableColumns
import android.widget.Toast
import com.theformofvoid.voidbridge.core.Content
import com.theformofvoid.voidbridge.core.Protocol

/**
 * "Share → Send to a device": sends any files to one of your devices. The
 * device comes from a sharing shortcut or [Shortcuts.EXTRA_DEVICE], or is
 * picked from a list.
 */
class SendFileActivity : Activity() {
    private lateinit var prefs: Prefs

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        prefs = Prefs(this)
        val service = SyncService.instance
        val uris = streams()
        when {
            service == null -> toast(getString(R.string.not_running))
            uris.isEmpty() -> {
                // Shared text has no file; send it to the clipboards instead.
                val text = intent.getStringExtra(Intent.EXTRA_TEXT)
                if (text.isNullOrEmpty()) toast(getString(R.string.nothing_to_send)) else {
                    service.onLocalContent(Content(Protocol.CLIP_TEXT, text = text), manual = true)
                    toast(getString(R.string.sent))
                }
            }
            else -> {
                val target = intent.getStringExtra(Shortcuts.EXTRA_DEVICE)
                    ?: if (Build.VERSION.SDK_INT >= 29) Shortcuts.deviceFor(intent.getStringExtra(Intent.EXTRA_SHORTCUT_ID)) else null
                if (target != null) send(service, target, uris) else choose(service, uris)
                return
            }
        }
        finish()
    }

    private fun toast(s: String) = Toast.makeText(this, s, Toast.LENGTH_LONG).show()

    @Suppress("DEPRECATION")
    private fun streams(): List<Uri> = when (intent.action) {
        Intent.ACTION_SEND -> listOfNotNull(
            if (Build.VERSION.SDK_INT >= 33) intent.getParcelableExtra(Intent.EXTRA_STREAM, Uri::class.java) else intent.getParcelableExtra(Intent.EXTRA_STREAM),
        )
        Intent.ACTION_SEND_MULTIPLE ->
            (if (Build.VERSION.SDK_INT >= 33) intent.getParcelableArrayListExtra(Intent.EXTRA_STREAM, Uri::class.java) else intent.getParcelableArrayListExtra(Intent.EXTRA_STREAM))
                ?.filterNotNull() ?: emptyList()
        else -> emptyList()
    }

    private fun dialogTheme() =
        if (resources.configuration.uiMode and Configuration.UI_MODE_NIGHT_MASK == Configuration.UI_MODE_NIGHT_YES) android.R.style.Theme_DeviceDefault_Dialog_Alert
        else android.R.style.Theme_DeviceDefault_Light_Dialog_Alert

    private fun choose(service: SyncService, uris: List<Uri>) {
        val online = service.status().devices
        val onlineIds = online.map { it.id }.toSet()
        val offline = prefs.knownDevices().filter { it.id !in onlineIds }.sortedByDescending { it.seen }
        val ids = online.map { it.id } + offline.map { it.id }
        val labels = online.map { it.name } + offline.map { getString(R.string.device_offline, it.name) }
        val title = if (uris.size == 1) getString(R.string.send_one_to) else getString(R.string.send_many_to, uris.size)
        val b = AlertDialog.Builder(this, dialogTheme()).setTitle(title)
        if (ids.isEmpty()) b.setMessage(R.string.no_devices_yet).setPositiveButton(android.R.string.ok, null)
        else b.setItems(labels.toTypedArray()) { _, i -> send(service, ids[i], uris) }.setNegativeButton(android.R.string.cancel, null)
        b.setOnDismissListener { if (!sending) finish() }.show()
    }

    @Volatile private var sending = false

    private fun send(service: SyncService, device: String, uris: List<Uri>) {
        sending = true
        val name = service.status().devices.firstOrNull { it.id == device }?.name
            ?: prefs.knownDevices().firstOrNull { it.id == device }?.name ?: device
        // Open the files while this screen still holds the share's read permission.
        Thread {
            val items = uris.mapNotNull { open(it) }
            runOnUiThread {
                if (items.isEmpty()) toast(getString(R.string.cant_read_files))
                else {
                    service.sendFiles(device, name, items)
                    toast(getString(R.string.sending_to, name))
                }
                finish()
            }
        }.start()
    }

    private fun open(uri: Uri): Outgoing? = try {
        var name = uri.lastPathSegment?.substringAfterLast('/') ?: "file"
        contentResolver.query(uri, arrayOf(OpenableColumns.DISPLAY_NAME), null, null, null)?.use { c ->
            if (c.moveToFirst() && !c.isNull(0)) name = c.getString(0)
        }
        val pfd = contentResolver.openFileDescriptor(uri, "r")
        pfd?.let { Outgoing(name, it.statSize, contentResolver.getType(uri) ?: "", it) } // statSize is -1 for streams
    } catch (e: Exception) {
        null
    }
}
