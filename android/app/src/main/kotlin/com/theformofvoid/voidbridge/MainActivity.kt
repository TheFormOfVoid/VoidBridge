package com.theformofvoid.voidbridge

import android.Manifest
import android.annotation.SuppressLint
import android.app.Activity
import android.app.AlertDialog
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
import android.widget.Switch
import android.widget.TextView
import android.widget.Toast
import com.theformofvoid.voidbridge.core.Identity
import com.theformofvoid.voidbridge.core.Keys
import com.theformofvoid.voidbridge.core.Protocol
import com.theformofvoid.voidbridge.core.ServerApi

class MainActivity : Activity() {
    companion object {
        private const val REQ_FILES = 10
        private const val REQ_FOLDER = 11
    }

    private lateinit var prefs: Prefs
    private val adbCommand by lazy { "adb shell pm grant $packageName android.permission.READ_LOGS" }
    private var serverTab = true

    private fun <T : View> v(id: Int): T = findViewById(id)

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(R.layout.activity_main)
        prefs = Prefs(this)

        v<TextView>(R.id.adb_cmd).text = adbCommand
        v<EditText>(R.id.manual).setText(prefs.manual)

        v<Button>(R.id.tab_server).setOnClickListener { serverTab = true; refresh() }
        v<Button>(R.id.tab_code).setOnClickListener { serverTab = false; refresh() }
        v<EditText>(R.id.server).setOnFocusChangeListener { _, has -> if (!has) checkServer() }
        v<Button>(R.id.sign_in).setOnClickListener { account(create = false) }
        v<Button>(R.id.register).setOnClickListener { account(create = true) }
        v<Button>(R.id.create_code).setOnClickListener { joinCode(Protocol.newGroupCode(), it as Button) }
        v<Button>(R.id.join).setOnClickListener {
            val c = v<EditText>(R.id.code).text.toString()
            if (!Protocol.validCode(c)) Toast.makeText(this, R.string.bad_code, Toast.LENGTH_LONG).show() else joinCode(c, it as Button)
        }
        v<Button>(R.id.leave).setOnClickListener { leave() }

        v<Switch>(R.id.sync_switch).setOnCheckedChangeListener { _, on ->
            if (prefs.paused == !on) return@setOnCheckedChangeListener
            prefs.paused = !on
            SyncService.instance?.setPaused(!on)
            refresh()
        }
        v<Switch>(R.id.direct_switch).setOnCheckedChangeListener { _, on ->
            if (prefs.direct == on) return@setOnCheckedChangeListener
            prefs.direct = on
            restartSync()
        }
        v<Switch>(R.id.sensitive_switch).setOnCheckedChangeListener { _, on ->
            prefs.skipSensitive = on
            SyncService.instance?.setSkipSensitive(on)
        }
        v<EditText>(R.id.manual).setOnFocusChangeListener { view, has ->
            if (!has) {
                prefs.manual = (view as EditText).text.toString()
                SyncService.instance?.setManual(prefs.manualList)
            }
        }

        v<Button>(R.id.notif_btn).setOnClickListener {
            if (Build.VERSION.SDK_INT >= 33) requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), 1)
        }
        v<Button>(R.id.battery_btn).setOnClickListener { requestBatteryExemption() }
        v<Button>(R.id.overlay_btn).setOnClickListener {
            startActivity(Intent(Settings.ACTION_MANAGE_OVERLAY_PERMISSION, Uri.parse("package:$packageName")))
        }

        v<Button>(R.id.send_file).setOnClickListener { pickFiles(null) }
        v<Button>(R.id.folder_change).setOnClickListener {
            try {
                startActivityForResult(Intent(Intent.ACTION_OPEN_DOCUMENT_TREE), REQ_FOLDER)
            } catch (_: Exception) {
                Toast.makeText(this, R.string.no_folder_picker, Toast.LENGTH_LONG).show()
            }
        }
        v<Button>(R.id.folder_reset).setOnClickListener {
            releaseFolder()
            prefs.receiveTree = ""
            refresh()
        }

        if (prefs.enabled && prefs.configured) SyncService.start(this)
        handleSendTo(intent)
    }

    override fun onNewIntent(intent: Intent) {
        super.onNewIntent(intent)
        handleSendTo(intent)
    }

    // ---- files ----

    private var pickTarget: String? = null

    /** A device shortcut from the launcher: pick files for that device. */
    private fun handleSendTo(i: Intent?) {
        if (i?.action == Shortcuts.ACTION_SEND_TO) pickFiles(i.getStringExtra(Shortcuts.EXTRA_DEVICE))
    }

    private fun pickFiles(device: String?) {
        pickTarget = device
        val i = Intent(Intent.ACTION_OPEN_DOCUMENT).addCategory(Intent.CATEGORY_OPENABLE).setType("*/*").putExtra(Intent.EXTRA_ALLOW_MULTIPLE, true)
        startActivityForResult(i, REQ_FILES)
    }

    @Deprecated("Deprecated in Java")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (resultCode != RESULT_OK || data == null) return
        when (requestCode) {
            REQ_FILES -> {
                val uris = data.clipData?.let { c -> List(c.itemCount) { c.getItemAt(it).uri } } ?: listOfNotNull(data.data)
                if (uris.isEmpty()) return
                startActivity(
                    Intent(this, SendFileActivity::class.java)
                        .setAction(Intent.ACTION_SEND_MULTIPLE)
                        .putParcelableArrayListExtra(Intent.EXTRA_STREAM, ArrayList(uris))
                        .apply { pickTarget?.let { putExtra(Shortcuts.EXTRA_DEVICE, it) } },
                )
            }
            REQ_FOLDER -> {
                val tree = data.data ?: return
                try {
                    contentResolver.takePersistableUriPermission(tree, Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_GRANT_WRITE_URI_PERMISSION)
                } catch (e: Exception) {
                    Toast.makeText(this, e.message, Toast.LENGTH_LONG).show()
                    return
                }
                releaseFolder()
                prefs.receiveTree = tree.toString()
                refresh()
            }
        }
    }

    private fun releaseFolder() {
        val old = prefs.receiveTree.takeIf { it.isNotEmpty() } ?: return
        try {
            contentResolver.releasePersistableUriPermission(Uri.parse(old), Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_GRANT_WRITE_URI_PERMISSION)
        } catch (_: Exception) {
        }
    }

    override fun onResume() {
        super.onResume()
        SyncService.statusListener = { refresh() }
        // Android 13+ asks about log access only while we're visible.
        SyncService.instance?.startLogcatWatcher(restart = true)
        if (prefs.configured) Shortcuts.publish(this, prefs) // not rate limited while visible
        refresh()
    }

    override fun onPause() {
        SyncService.statusListener = null
        super.onPause()
    }

    // ---- joining ----

    private fun me() = Identity(prefs.deviceId, Settings.Global.getString(contentResolver, Settings.Global.DEVICE_NAME) ?: Build.MODEL)

    private fun busy(b: Button, work: () -> Unit, done: (Throwable?) -> Unit) {
        val label = b.text
        b.isEnabled = false
        b.setText(R.string.working)
        Thread {
            val err = try { work(); null } catch (e: Throwable) { e }
            runOnUiThread {
                b.isEnabled = true
                b.text = label
                done(err)
            }
        }.start()
    }

    private fun checkServer() {
        val addr = v<EditText>(R.id.server).text.toString()
        if (addr.isBlank()) return
        val out = v<TextView>(R.id.server_info)
        out.text = "…"
        Thread {
            val text = try {
                val info = ServerApi(ServerApi.normalizeUrl(addr)).info()
                "✓ ${info.name}" + when {
                    !info.hasUsers -> ": no accounts yet. The account you create becomes the admin."
                    info.signup == "open" -> ": anyone can create an account."
                    else -> ": new accounts need an invite code."
                }
            } catch (e: Exception) {
                "✗ ${e.message}"
            }
            runOnUiThread { out.text = text }
        }.start()
    }

    private fun account(create: Boolean) {
        val addr = v<EditText>(R.id.server).text.toString()
        val user = Protocol.normalizeUsername(v<EditText>(R.id.username).text.toString())
        val pass = v<EditText>(R.id.password).text.toString()
        val invite = v<EditText>(R.id.invite).text.toString()
        if (create && pass.length < 8) {
            Toast.makeText(this, "Use a password of at least 8 characters.", Toast.LENGTH_LONG).show()
            return
        }
        val button = v<Button>(if (create) R.id.register else R.id.sign_in)
        var base = ""
        var master = ByteArray(0)
        var token = ""
        busy(button, {
            base = ServerApi.normalizeUrl(addr)
            val api = ServerApi(base)
            api.info()
            master = Protocol.masterFromAccount(user, pass) // PBKDF2, takes a moment
            val keys = Keys(master)
            token = (if (create) api.register(user, keys, invite, me()) else api.login(user, keys, me())).token
        }) { err ->
            if (err != null) {
                Toast.makeText(this, err.message ?: err.toString(), Toast.LENGTH_LONG).show()
                return@busy
            }
            prefs.leave()
            prefs.mode = SyncService.MODE_ACCOUNT
            prefs.server = base
            prefs.username = user
            prefs.token = token
            prefs.master = master
            v<EditText>(R.id.password).setText("")
            afterJoin()
        }
    }

    private fun joinCode(code: String, button: Button) {
        var master = ByteArray(0)
        busy(button, { master = Protocol.masterFromCode(code) }) { err ->
            if (err != null) return@busy
            leaveServerQuietly()
            prefs.leave()
            prefs.mode = SyncService.MODE_CODE
            prefs.code = Protocol.formatCode(code)
            prefs.master = master
            v<EditText>(R.id.code).setText("")
            afterJoin()
        }
    }

    private fun afterJoin() {
        prefs.enabled = true
        restartSync()
        if (!notificationsAllowed()) v<Button>(R.id.notif_btn).performClick()
    }

    private fun leaveServerQuietly() {
        if (prefs.mode == SyncService.MODE_ACCOUNT && prefs.token.isNotEmpty()) {
            val api = ServerApi(prefs.server, prefs.token)
            Thread { try { api.logout() } catch (_: Exception) {} }.start()
        }
    }

    private fun leave() {
        AlertDialog.Builder(this)
            .setTitle(R.string.leave)
            .setMessage("This device will stop syncing until you sign in or join again.")
            .setPositiveButton(R.string.leave) { _, _ ->
                leaveServerQuietly()
                prefs.leave()
                SyncService.stop(this)
                refresh()
            }
            .setNegativeButton(android.R.string.cancel, null)
            .show()
    }

    private fun restartSync() {
        SyncService.stop(this)
        v<View>(R.id.status).postDelayed({
            if (prefs.configured && prefs.enabled) SyncService.start(this)
            refresh()
        }, 300)
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

    // ---- rendering ----

    private fun refresh() {
        val configured = prefs.configured
        val st = SyncService.instance?.status()
        val status = v<TextView>(R.id.status)
        status.text = when {
            !configured -> getString(R.string.status_unpaired)
            st == null -> getString(R.string.status_stopped)
            else -> st.text
        }
        status.setCompoundDrawablesRelativeWithIntrinsicBounds(getDrawable(R.drawable.dot)?.mutate()?.apply {
            setTint(getColor(if (st?.connected == true && !prefs.paused) R.color.ok else if (prefs.paused) R.color.warn else R.color.bad))
        }, null, null, null)
        v<TextView>(R.id.devices).text = st?.devices?.joinToString("\n") { "• ${it.name}  (${it.via.joinToString(", ")})" } ?: ""
        val problems = listOfNotNull(st?.peerErr, st?.serverErr?.takeIf { st.serverUp.not() && prefs.mode == SyncService.MODE_ACCOUNT })
        v<TextView>(R.id.problems).apply {
            text = problems.joinToString("\n")
            visibility = if (problems.isEmpty()) View.GONE else View.VISIBLE
        }
        v<Switch>(R.id.sync_switch).apply {
            isChecked = !prefs.paused
            isEnabled = configured
        }

        v<View>(R.id.setup_section).visibility = if (configured) View.GONE else View.VISIBLE
        v<View>(R.id.group_section).visibility = if (configured) View.VISIBLE else View.GONE
        v<View>(R.id.server_box).visibility = if (serverTab) View.VISIBLE else View.GONE
        v<View>(R.id.code_box).visibility = if (serverTab) View.GONE else View.VISIBLE
        v<Button>(R.id.tab_server).alpha = if (serverTab) 1f else 0.5f
        v<Button>(R.id.tab_code).alpha = if (serverTab) 0.5f else 1f
        if (configured) {
            val account = prefs.mode == SyncService.MODE_ACCOUNT
            v<TextView>(R.id.group_summary).text =
                if (account) getString(R.string.summary_account, prefs.username, prefs.server) else getString(R.string.summary_code)
            v<TextView>(R.id.group_code).apply {
                text = prefs.code
                visibility = if (account) View.GONE else View.VISIBLE
            }
        }
        v<Button>(R.id.send_file).visibility = if (st?.connected == true) View.VISIBLE else View.GONE
        v<TextView>(R.id.folder).text = getString(R.string.folder_now, Files.folderLabel(this, prefs))
        v<Button>(R.id.folder_reset).visibility = if (prefs.receiveTree.isEmpty()) View.GONE else View.VISIBLE
        v<Switch>(R.id.direct_switch).isChecked = prefs.direct
        v<Switch>(R.id.sensitive_switch).isChecked = prefs.skipSensitive

        step(R.id.notif_btn, notificationsAllowed())
        step(R.id.battery_btn, getSystemService(PowerManager::class.java).isIgnoringBatteryOptimizations(packageName))
        step(R.id.overlay_btn, Settings.canDrawOverlays(this))
        val logsNeeded = Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q
        val logsDone = !logsNeeded || SyncService.hasReadLogs(this)
        v<TextView>(R.id.logs_text).setText(
            when {
                !logsNeeded -> R.string.step_logs_old
                logsDone -> R.string.step_logs_done
                else -> R.string.step_logs
            },
        )
        v<View>(R.id.adb_cmd).visibility = if (logsDone) View.GONE else View.VISIBLE
    }

    private fun step(buttonId: Int, done: Boolean) {
        v<Button>(buttonId).apply {
            isEnabled = !done
            setText(if (done) R.string.done else R.string.allow)
        }
    }
}
