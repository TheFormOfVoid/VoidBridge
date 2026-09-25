package com.theformofvoid.voidbridge

import android.Manifest
import android.annotation.SuppressLint
import android.app.Activity
import android.content.ClipData
import android.content.ClipboardManager
import android.content.Intent
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.PowerManager
import android.provider.Settings
import android.view.View
import android.widget.Button
import android.widget.EditText
import android.widget.TextView
import android.widget.Toast
import com.theformofvoid.voidbridge.core.Protocol

class MainActivity : Activity() {
    private lateinit var prefs: Prefs
    private lateinit var status: TextView
    private lateinit var code: EditText
    private lateinit var host: EditText
    private lateinit var pair: Button
    private lateinit var toggle: Button

    private val adbCommand by lazy { "adb shell pm grant $packageName android.permission.READ_LOGS" }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)
        prefs = Prefs(this)
        status = findViewById(R.id.status)
        code = findViewById(R.id.code)
        host = findViewById(R.id.host)
        pair = findViewById(R.id.pair)
        toggle = findViewById(R.id.toggle)

        host.setText(prefs.manualHost)
        if (prefs.paired) code.hint = "•••• •••• •••• ••••  (paired — enter a new code to re-pair)"
        findViewById<TextView>(R.id.adb_cmd).text = adbCommand

        pair.setOnClickListener { onPair() }
        toggle.setOnClickListener {
            if (SyncService.instance != null) {
                SyncService.stop(this)
            } else if (prefs.paired) {
                prefs.enabled = true
                SyncService.start(this)
            }
            refresh()
        }
        findViewById<Button>(R.id.notif_btn).setOnClickListener {
            if (Build.VERSION.SDK_INT >= 33) requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1)
        }
        findViewById<Button>(R.id.battery_btn).setOnClickListener { requestBatteryExemption() }
        findViewById<Button>(R.id.overlay_btn).setOnClickListener {
            startActivity(Intent(Settings.ACTION_MANAGE_OVERLAY_PERMISSION, Uri.parse("package:$packageName")))
        }
        findViewById<Button>(R.id.logs_btn).setOnClickListener {
            getSystemService(ClipboardManager::class.java).setPrimaryClip(ClipData.newPlainText("adb", adbCommand))
            Toast.makeText(this, R.string.copied, Toast.LENGTH_LONG).show()
        }

        if (prefs.enabled && prefs.paired) SyncService.start(this)
    }

    override fun onResume() {
        super.onResume()
        SyncService.statusListener = { _, _ -> refresh() }
        // On Android 13+ log access must be approved while we're in the foreground,
        // so (re)start the watcher now that we are.
        SyncService.instance?.startLogcatWatcher(restart = true)
        refresh()
    }

    override fun onPause() {
        SyncService.statusListener = null
        super.onPause()
    }

    private fun onPair() {
        val c = code.text.toString()
        prefs.manualHost = host.text.toString()
        if (c.isBlank() && prefs.paired) {
            // Only the address changed.
            restartSync()
            return
        }
        if (!Protocol.validCode(c)) {
            Toast.makeText(this, R.string.bad_code, Toast.LENGTH_LONG).show()
            return
        }
        pair.isEnabled = false
        pair.setText(R.string.pairing)
        Thread {
            val key = Protocol.deriveKey(c) // PBKDF2, takes a moment
            runOnUiThread {
                prefs.pairingKey = key
                prefs.lastHost = ""
                code.setText("")
                code.hint = "•••• •••• •••• ••••  (paired — enter a new code to re-pair)"
                pair.isEnabled = true
                pair.setText(R.string.pair)
                restartSync()
                if (!notificationsAllowed()) findViewById<Button>(R.id.notif_btn).performClick()
            }
        }.start()
    }

    private fun restartSync() {
        if (SyncService.instance != null) SyncService.stop(this)
        prefs.enabled = true
        status.postDelayed({ SyncService.start(this); refresh() }, 300)
    }

    @SuppressLint("BatteryLife")
    private fun requestBatteryExemption() {
        try {
            startActivity(Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS, Uri.parse("package:$packageName")))
        } catch (_: Exception) {
            startActivity(Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS))
        }
    }

    private fun notificationsAllowed() = Build.VERSION.SDK_INT < 33 ||
        checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED

    private fun refresh() {
        val running = SyncService.instance != null
        val text = when {
            !prefs.paired -> getString(R.string.status_unpaired)
            !running -> getString(R.string.status_stopped)
            else -> SyncService.lastStatus.ifEmpty { getString(R.string.status_starting) }
        }
        status.text = text
        status.setCompoundDrawablesRelativeWithIntrinsicBounds(getDrawable(R.drawable.dot)?.mutate()?.apply {
            setTint(getColor(if (running && SyncService.lastConnected) R.color.ok else R.color.bad))
        }, null, null, null)

        toggle.visibility = if (prefs.paired) View.VISIBLE else View.GONE
        toggle.setText(if (running) R.string.stop else R.string.start)

        step(R.id.notif_btn, notificationsAllowed())
        step(R.id.battery_btn, getSystemService(PowerManager::class.java).isIgnoringBatteryOptimizations(packageName))
        step(R.id.overlay_btn, Settings.canDrawOverlays(this))

        val logsText = findViewById<TextView>(R.id.logs_text)
        val logsNeeded = Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q
        val logsDone = !logsNeeded || SyncService.hasReadLogs(this)
        logsText.setText(
            when {
                !logsNeeded -> R.string.step_logs_old
                logsDone -> R.string.step_logs_done
                else -> R.string.step_logs
            },
        )
        findViewById<View>(R.id.adb_cmd).visibility = if (logsDone) View.GONE else View.VISIBLE
        findViewById<View>(R.id.logs_btn).visibility = if (logsDone) View.GONE else View.VISIBLE
    }

    private fun step(buttonId: Int, done: Boolean) {
        findViewById<Button>(buttonId).apply {
            isEnabled = !done
            setText(if (done) R.string.done else R.string.allow)
        }
    }
}
