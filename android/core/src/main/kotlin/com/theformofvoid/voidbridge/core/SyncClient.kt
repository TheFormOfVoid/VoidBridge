package com.theformofvoid.voidbridge.core

import java.io.IOException
import java.net.InetSocketAddress
import java.net.Socket
import java.util.concurrent.atomic.AtomicBoolean

/** Where the client should try to connect, most preferred first. */
fun interface HostSource {
    fun candidates(): List<InetSocketAddress>
}

/**
 * The phone side of VoidBridge. Keeps one connection to the PC alive forever:
 * reconnects with a short backoff, resends the newest unacknowledged local
 * clip after every reconnect, and ignores remote clips older than our own
 * latest copy.
 */
class SyncClient(
    private val pairingKey: ByteArray,
    private val deviceId: String,
    private val deviceName: String,
    private val hosts: HostSource,
    private val listener: Listener,
) {
    interface Listener {
        /** Put text on the phone clipboard. Called on the client's thread. */
        fun applyRemoteClip(text: String)
        fun onStatus(status: Status)
        /** The last address that worked, so the app can remember it. */
        fun onConnectedTo(address: InetSocketAddress) {}
        fun log(msg: String) {}
    }

    sealed class Status {
        object Searching : Status()
        data class Connecting(val address: String) : Status()
        data class Connected(val pcName: String, val address: String) : Status()
        data class Failed(val reason: String) : Status()
    }

    private class Clip(val id: String, val time: Long, val text: String)

    // Tunables (ms).
    var connectTimeout = 3_000
    var readTimeout = 25_000
    var handshakeTimeout = 10_000
    var maxBackoff = 5_000L

    private val lock = Object()
    private val running = AtomicBoolean(false)
    private var thread: Thread? = null
    private var conn: Connection? = null
    private var pending: Clip? = null // newest local clip the PC hasn't acked
    private var lastLocalTime = 0L // when the user last copied on this phone
    private var lastHash: String? = null
    private var wakeup = false

    fun start() {
        if (!running.compareAndSet(false, true)) return
        thread = Thread(::loop, "voidbridge-sync").apply { isDaemon = true; start() }
    }

    fun stop() {
        running.set(false)
        synchronized(lock) { conn?.close(); lock.notifyAll() }
        thread?.interrupt()
    }

    /** Skip the current backoff and retry now, e.g. when the network changes. */
    fun kick() {
        synchronized(lock) { wakeup = true; lock.notifyAll() }
    }

    /** Force the current connection closed so we reconnect (network switched). */
    fun reconnect() {
        synchronized(lock) { conn?.close() }
        kick()
    }

    val isConnected: Boolean get() = synchronized(lock) { conn != null }

    /** Report text the user copied on the phone. Safe to call from any thread. */
    fun localClip(text: String) {
        if (text.isEmpty() || text.length > Protocol.MAX_TEXT) return
        val h = Protocol.sha256(text)
        val clip: Clip
        val c: Connection?
        synchronized(lock) {
            if (h == lastHash) return // echo of a clip we just applied, or a repeat
            lastHash = h
            clip = Clip(Protocol.newId(), System.currentTimeMillis(), text)
            lastLocalTime = clip.time
            pending = clip
            c = conn
        }
        c?.let { Thread { send(it, clip) }.start() }
    }

    private fun send(c: Connection, clip: Clip) {
        try {
            c.send(Message(Message.CLIP, id = clip.id, time = clip.time, text = clip.text))
        } catch (e: IOException) {
            c.close() // the read loop notices and reconnects; the clip stays pending
        }
    }

    private fun loop() {
        var backoff = 250L
        while (running.get()) {
            val candidates = try {
                hosts.candidates()
            } catch (e: Exception) {
                emptyList()
            }
            if (candidates.isEmpty()) listener.onStatus(Status.Searching)
            var connected = false
            for (addr in candidates) {
                if (!running.get()) return
                val c = tryConnect(addr) ?: continue
                connected = true
                backoff = 250L
                listener.onConnectedTo(addr)
                session(c, addr)
                break
            }
            if (!running.get()) return
            if (!connected) {
                sleep(backoff)
                backoff = (backoff * 2).coerceAtMost(maxBackoff)
            } else {
                sleep(250) // brief pause before reconnecting after a drop
            }
        }
    }

    private fun tryConnect(addr: InetSocketAddress): Connection? {
        listener.onStatus(Status.Connecting(addr.hostString))
        val s = Socket()
        return try {
            s.tcpNoDelay = true
            s.keepAlive = true
            s.connect(addr, connectTimeout)
            Connection.handshake(s, pairingKey, deviceId, deviceName, handshakeTimeout).also {
                // The PC pings every 10s; hearing nothing for this long means the link is dead.
                s.soTimeout = readTimeout
            }
        } catch (e: AuthException) {
            s.close()
            listener.onStatus(Status.Failed("Wrong pairing code for ${addr.hostString}"))
            listener.log("auth failed at $addr")
            null
        } catch (e: Exception) {
            s.close()
            listener.log("connect $addr: $e")
            null
        }
    }

    private fun session(c: Connection, addr: InetSocketAddress) {
        val resend: Clip?
        synchronized(lock) {
            conn = c
            resend = pending
        }
        listener.onStatus(Status.Connected(c.peerName.ifEmpty { "PC" }, addr.hostString))
        listener.log("connected to ${c.peerName} at $addr")
        try {
            resend?.let { send(c, it) }
            while (running.get()) {
                val m = c.recv()
                when (m.type) {
                    Message.PING -> c.send(Message(Message.PONG))
                    Message.ACK -> synchronized(lock) {
                        if (pending?.id == m.id) pending = null
                    }
                    Message.CLIP -> onRemoteClip(c, m)
                }
            }
        } catch (e: Exception) {
            listener.log("disconnected: $e")
        } finally {
            c.close()
            synchronized(lock) { if (conn === c) conn = null }
        }
    }

    private fun onRemoteClip(c: Connection, m: Message) {
        c.send(Message(Message.ACK, id = m.id))
        val remoteTime = m.time - c.clockOffset
        synchronized(lock) {
            // Our own newer copy wins; it's still pending and reaches the PC.
            if (remoteTime < lastLocalTime) return
            val h = Protocol.sha256(m.text)
            if (h == lastHash) return
            lastHash = h
            pending = null
        }
        listener.applyRemoteClip(m.text)
    }

    private fun sleep(ms: Long) {
        synchronized(lock) {
            if (!wakeup) {
                try {
                    lock.wait(ms)
                } catch (_: InterruptedException) {
                }
            }
            wakeup = false
        }
    }
}
