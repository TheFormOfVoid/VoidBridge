package com.theformofvoid.voidbridge

import android.content.Context
import com.theformofvoid.voidbridge.core.Device
import com.theformofvoid.voidbridge.core.Keys
import com.theformofvoid.voidbridge.core.Protocol
import com.theformofvoid.voidbridge.core.hexToBytes
import com.theformofvoid.voidbridge.core.toHex
import org.json.JSONObject

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

    /** Folder chosen for received files (a document tree URI), or "" for Download/VoidBridge. */
    var receiveTree: String
        get() = sp.getString("receive_tree", "") ?: ""
        set(v) = sp.edit().putString("receive_tree", v).apply()

    data class Known(val id: String, val name: String, val kind: String, val seen: Long)

    /** Devices seen recently, for sending files to. */
    fun knownDevices(): List<Known> {
        val o = try { JSONObject(sp.getString("known", "{}") ?: "{}") } catch (_: Exception) { JSONObject() }
        return o.keys().asSequence().mapNotNull { id ->
            o.optJSONObject(id)?.let { Known(id, it.optString("name", id), it.optString("kind"), it.optLong("seen")) }
        }.toList()
    }

    /** Records connected devices; true if the list of names changed. */
    @Synchronized
    fun remember(devices: List<Device>): Boolean {
        val now = System.currentTimeMillis()
        val before = knownDevices().associateBy { it.id }
        val after = before.toMutableMap()
        for (d in devices) after[d.id] = Known(d.id, d.name, d.kind, now)
        after.values.removeAll { now - it.seen > 60L * 24 * 3600 * 1000 }
        val o = JSONObject()
        for (k in after.values) o.put(k.id, JSONObject().put("name", k.name).put("kind", k.kind).put("seen", k.seen))
        sp.edit().putString("known", o.toString()).apply()
        return before.mapValues { it.value.name } != after.mapValues { it.value.name }
    }

    fun leave() {
        sp.edit().remove("mode").remove("code").remove("master").remove("server").remove("username").remove("token").remove("known").apply()
    }
}
