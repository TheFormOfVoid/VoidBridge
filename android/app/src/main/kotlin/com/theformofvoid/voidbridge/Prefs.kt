package com.theformofvoid.voidbridge

import android.content.Context
import com.theformofvoid.voidbridge.core.Keys
import com.theformofvoid.voidbridge.core.Protocol
import com.theformofvoid.voidbridge.core.hexToBytes
import com.theformofvoid.voidbridge.core.toHex

/** Persistent settings. Passwords are never stored, only the derived key. */
class Prefs(context: Context) {
    private val sp = context.getSharedPreferences("voidbridge2", Context.MODE_PRIVATE)

    val deviceId: String
        get() = sp.getString("device_id", null) ?: ("phone-" + Protocol.newId()).also {
            sp.edit().putString("device_id", it).apply()
        }

    /** "", "code" or "account". */
    var mode: String
        get() = sp.getString("mode", "") ?: ""
        set(v) = sp.edit().putString("mode", v).apply()

    var code: String
        get() = sp.getString("code", "") ?: ""
        set(v) = sp.edit().putString("code", v).apply()

    var master: ByteArray?
        get() = sp.getString("master", null)?.hexToBytes()
        set(v) = sp.edit().putString("master", v?.toHex()).apply()

    val keys: Keys? get() = master?.let { Keys(it) }

    var server: String
        get() = sp.getString("server", "") ?: ""
        set(v) = sp.edit().putString("server", v).apply()

    var username: String
        get() = sp.getString("username", "") ?: ""
        set(v) = sp.edit().putString("username", v).apply()

    var token: String
        get() = sp.getString("token", "") ?: ""
        set(v) = sp.edit().putString("token", v).apply()

    val configured get() = mode.isNotEmpty() && master != null

    /** Whether the user wants sync running (survives reboots). */
    var enabled: Boolean
        get() = sp.getBoolean("enabled", true)
        set(v) = sp.edit().putBoolean("enabled", v).apply()

    var paused: Boolean
        get() = sp.getBoolean("paused", false)
        set(v) = sp.edit().putBoolean("paused", v).apply()

    var direct: Boolean
        get() = sp.getBoolean("direct", true)
        set(v) = sp.edit().putBoolean("direct", v).apply()

    var skipSensitive: Boolean
        get() = sp.getBoolean("skip_sensitive", true)
        set(v) = sp.edit().putBoolean("skip_sensitive", v).apply()

    /** Typed-in device addresses, comma separated. */
    var manual: String
        get() = sp.getString("manual", "") ?: ""
        set(v) = sp.edit().putString("manual", v).apply()

    val manualList: List<String> get() = manual.split(',', ' ', '\n').map { it.trim() }.filter { it.isNotEmpty() }

    fun leave() {
        sp.edit().remove("mode").remove("code").remove("master").remove("server").remove("username").remove("token").apply()
    }
}
