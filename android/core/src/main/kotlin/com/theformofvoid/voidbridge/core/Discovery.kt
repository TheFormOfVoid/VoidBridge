package com.theformofvoid.voidbridge.core

import org.json.JSONObject
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.NetworkInterface
import java.net.SocketException
import java.net.SocketTimeoutException

/**
 * Listens for the PC's UDP beacons (see pc/internal/discovery) and remembers
 * where PCs with our pairing fingerprint were last seen.
 */
class Discovery(private val fingerprint: String, private val log: (String) -> Unit = {}) {
    private data class Seen(val address: InetSocketAddress, val at: Long)

    @Volatile private var running = false
    private var socket: DatagramSocket? = null
    private val seen = LinkedHashMap<String, Seen>() // by PC id

    fun start() {
        if (running) return
        running = true
        Thread(::listen, "voidbridge-discovery").apply { isDaemon = true; start() }
    }

    fun stop() {
        running = false
        socket?.close()
    }

    /** PCs seen in the last [maxAgeMs], newest first. */
    fun recent(maxAgeMs: Long = 30_000): List<InetSocketAddress> = synchronized(seen) {
        val now = System.currentTimeMillis()
        seen.values.filter { now - it.at <= maxAgeMs }.sortedByDescending { it.at }.map { it.address }
    }

    /** Ask PCs to announce themselves now instead of waiting for the next beacon. */
    fun probe() {
        val s = socket ?: return
        val payload = JSONObject().put("app", "voidbridge").put("type", "probe").toString().toByteArray()
        for (dst in broadcastAddresses()) {
            try {
                s.send(DatagramPacket(payload, payload.size, dst, Protocol.DISCOVERY_PORT))
            } catch (_: Exception) {
            }
        }
    }

    private fun listen() {
        while (running) {
            try {
                DatagramSocket(null).use { s ->
                    s.reuseAddress = true
                    s.broadcast = true
                    s.soTimeout = 5_000
                    s.bind(InetSocketAddress(Protocol.DISCOVERY_PORT))
                    socket = s
                    probe()
                    val buf = ByteArray(1500)
                    while (running) {
                        val p = DatagramPacket(buf, buf.size)
                        try {
                            s.receive(p)
                        } catch (_: SocketTimeoutException) {
                            continue
                        }
                        handle(String(p.data, 0, p.length, Charsets.UTF_8), p.address)
                    }
                }
            } catch (e: SocketException) {
                if (!running) return
                log("discovery socket: $e")
                Thread.sleep(2_000)
            }
        }
    }

    private fun handle(json: String, from: InetAddress) {
        val o = try {
            JSONObject(json)
        } catch (_: Exception) {
            return
        }
        if (o.optString("app") != "voidbridge" || o.optString("type") == "probe") return
        if (o.optString("fp") != fingerprint) return // a PC we aren't paired with
        val port = o.optInt("port", Protocol.DEFAULT_PORT)
        val id = o.optString("id")
        synchronized(seen) {
            seen[id] = Seen(InetSocketAddress(from, port), System.currentTimeMillis())
        }
    }

    private fun broadcastAddresses(): List<InetAddress> {
        val out = mutableListOf<InetAddress>(InetAddress.getByName("255.255.255.255"))
        try {
            for (ni in NetworkInterface.getNetworkInterfaces()) {
                if (!ni.isUp || ni.isLoopback) continue
                ni.interfaceAddresses.mapNotNullTo(out) { it.broadcast }
            }
        } catch (_: Exception) {
        }
        return out
    }
}
