package com.theformofvoid.voidbridge

import android.content.Context
import com.theformofvoid.voidbridge.core.Protocol
import com.theformofvoid.voidbridge.core.hexToBytes
import com.theformofvoid.voidbridge.core.toHex
import java.net.InetSocketAddress

/** Persistent settings. The pairing code itself is never stored, only the derived key. */
class Prefs(context: Context) {
    private val sp = context.getSharedPreferences("voidbridge", Context.MODE_PRIVATE)

    val deviceId: String
        get() = sp.getString("device_id", null) ?: ("phone-" + Protocol.newId()).also {
            sp.edit().putString("device_id", it).apply()
        }

    var pairingKey: ByteArray?
        get() = sp.getString("pairing_key", null)?.hexToBytes()
        set(v) = sp.edit().putString("pairing_key", v?.toHex()).apply()

    val paired get() = pairingKey != null

    /** Whether the user wants sync running (survives reboots). */
    var enabled: Boolean
        get() = sp.getBoolean("enabled", false)
        set(v) = sp.edit().putBoolean("enabled", v).apply()

    /** Optional address the user typed, "host" or "host:port". */
    var manualHost: String
        get() = sp.getString("manual_host", "") ?: ""
        set(v) = sp.edit().putString("manual_host", v.trim()).apply()

    /** The last address that worked. */
    var lastHost: String
        get() = sp.getString("last_host", "") ?: ""
        set(v) = sp.edit().putString("last_host", v).apply()

    companion object {
        /** Parses "host" or "host:port"; resolves DNS, so call off the main thread. */
        fun parseHost(s: String): InetSocketAddress? {
            val t = s.trim()
            if (t.isEmpty()) return null
            val i = t.lastIndexOf(':')
            return try {
                if (i > 0 && t.indexOf(':') == i) {
                    InetSocketAddress(t.substring(0, i), t.substring(i + 1).toInt())
                } else {
                    InetSocketAddress(t, Protocol.DEFAULT_PORT)
                }
            } catch (_: Exception) {
                null
            }
        }
    }
}
