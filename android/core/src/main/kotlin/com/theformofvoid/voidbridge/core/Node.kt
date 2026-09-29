package com.theformofvoid.voidbridge.core

import org.json.JSONObject
import java.io.InputStream
import java.security.MessageDigest
import java.util.concurrent.Executors
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit

/** One clipboard value. */
class Content(
    val type: String,
    val text: String = "",
    val data: ByteArray = ByteArray(0),
    val mime: String = if (type == Protocol.CLIP_TEXT) "text/plain" else "image/png",
    val sensitive: Boolean = false,
) {
    fun bytes(): ByteArray = if (type == Protocol.CLIP_TEXT) text.toByteArray(Charsets.UTF_8) else data
    fun hash(): String = Protocol.hash(type, bytes())

    val valid: Boolean
        get() = when (type) {
            Protocol.CLIP_TEXT -> text.isNotEmpty() && text.toByteArray().size <= Protocol.MAX_TEXT
            Protocol.CLIP_IMAGE -> data.isNotEmpty() && data.size <= Protocol.MAX_IMAGE
            else -> false
        }
}

/** A connection a clip can travel over. */
interface Link {
    fun send(m: Message)
    fun close()
    val info: LinkInfo
}

data class LinkInfo(val peerId: String, val name: String, val kind: String, val via: String, val addr: String = "")

data class Device(val id: String, val name: String, val kind: String, val via: List<String>)

/**
 * The sync engine; mirrors internal/node in Go. New clips flood to every link,
 * duplicates are dropped by id, the newest clip wins, and each new link is
 * sent our current clip so devices catch up after being offline.
 */
class Node(
    val me: Identity,
    private val keys: Keys,
    private val listener: Listener,
) {
    interface Listener {
        /** Put a clip from another device on this device's clipboard. */
        fun apply(content: Content, from: Message)
        fun changed() {}
        fun log(msg: String) {}
    }

    @Volatile var paused = false
    @Volatile var skipSensitive = true

    private val lock = Any()
    private val links = LinkedHashSet<Link>()
    private val remote = HashMap<Link, List<PeerInfo>>()
    private var current: Message? = null
    private val seen = LinkedHashSet<String>()
    private var lastHash: String? = null
    @Volatile var lastSync = 0L
        private set

    private val sender = Executors.newCachedThreadPool { r -> Thread(r, "voidbridge-send").apply { isDaemon = true } }

    /** Where incoming files go; null refuses them. */
    @Volatile var fileReceiver: FileReceiver? = null
    /** How long a sender waits for the receiver to confirm a file. */
    @Volatile var fileAckTimeoutMs = 60_000L
    /** Incoming transfers that stop arriving for this long are dropped. */
    @Volatile var fileIdleTimeoutMs = 60_000L

    private class Incoming(val meta: FileMeta, val from: FileSender, val w: FileWriter) {
        var next = 0L
        var got = 0L
        val sum: MessageDigest = MessageDigest.getInstance("SHA-256")
        @Volatile var active = System.currentTimeMillis()
    }

    private val fileAcks = HashMap<String, LinkedBlockingQueue<String>>()
    private val incoming = HashMap<String, Incoming>()
    private val timer = Executors.newSingleThreadScheduledExecutor { r -> Thread(r, "voidbridge-files").apply { isDaemon = true } }.also {
        it.scheduleWithFixedDelay({ expireFiles() }, 5, 5, TimeUnit.SECONDS)
    }

    fun addLink(l: Link) {
        val cur = synchronized(lock) {
            links.add(l)
            current
        }
        cur?.let { send(l, it) }
        listener.changed()
    }

    fun removeLink(l: Link) {
        synchronized(lock) {
            links.remove(l)
            remote.remove(l)
        }
        listener.changed()
    }

    fun setRemoteDevices(l: Link, devices: List<PeerInfo>) {
        synchronized(lock) { if (l in links) remote[l] = devices }
        listener.changed()
    }

    private fun markSeen(id: String) {
        if (seen.add(id) && seen.size > 4096) seen.remove(seen.first())
    }

    /** A clip arrived on link l. */
    fun handle(l: Link, m: Message) {
        if (m.isFile) return handleFile(l, m)
        if (m.type != Message.CLIP) return
        synchronized(lock) {
            if (paused) return
            val dup = m.id in seen || !m.newerThan(current)
            markSeen(m.id)
            if (dup) return
        }
        val plain = Protocol.openClip(keys.content, m)
        if (plain == null) {
            listener.log("dropping clip ${m.id} from ${l.info.name}: can't decrypt")
            return
        }
        val c = when (m.clipType) {
            Protocol.CLIP_TEXT -> Content(Protocol.CLIP_TEXT, text = String(plain, Charsets.UTF_8), mime = m.mime)
            Protocol.CLIP_IMAGE -> Content(Protocol.CLIP_IMAGE, data = plain, mime = m.mime)
            else -> return
        }
        if (!c.valid) return
        val others: List<Link>
        val apply: Boolean
        synchronized(lock) {
            if (!m.newerThan(current)) return
            current = m
            val h = c.hash()
            apply = h != lastHash
            lastHash = h
            lastSync = System.currentTimeMillis()
            others = links.filter { it !== l }
        }
        others.forEach { send(it, m) }
        if (apply) listener.apply(c, m)
        listener.changed()
    }

    /** The user copied something on this device. */
    fun localCopy(c: Content) {
        if (!c.valid) return
        val h = c.hash()
        val m: Message
        synchronized(lock) {
            if (h == lastHash) return // our own write echoing back, or a repeat
            lastHash = h
            if (paused || (c.sensitive && skipSensitive)) return
            var now = System.currentTimeMillis()
            current?.let { if (now <= it.time) now = it.time + 1 }
            m = Message(Message.CLIP, id = Protocol.newId(), origin = me.id, originName = me.name, time = now, clipType = c.type, mime = c.mime)
        }
        val sealed = Protocol.sealClip(keys.content, m, c.bytes())
        val targets: List<Link>
        synchronized(lock) {
            current = sealed
            markSeen(sealed.id)
            targets = links.toList()
            if (targets.isNotEmpty()) lastSync = System.currentTimeMillis()
        }
        targets.forEach { send(it, sealed) }
        listener.changed()
    }

    /** Forget what's on the clipboard, e.g. before a manual "send now". */
    fun forgetLastHash() = synchronized(lock) { lastHash = null }

    private fun send(l: Link, m: Message) {
        sender.execute {
            try {
                l.send(m)
            } catch (e: Exception) {
                l.close()
            }
        }
    }

    // ---- files ----
    // Files go to one chosen device, directly if there's a link to it, otherwise
    // through the server. They're streamed in chunks, end-to-end encrypted, and
    // acknowledged by the receiver once complete. Mirrors internal/node/files.go.

    /** The best link to a device: direct first, then a server that has it online. */
    fun linkTo(id: String): Link? = synchronized(lock) {
        links.firstOrNull { it.info.peerId == id && it.info.kind != "server" }
            ?: remote.entries.firstOrNull { e -> e.value.any { it.id == id } }?.key
    }

    /**
     * Streams [input] (exactly meta.size bytes) to device [to] and blocks until
     * the receiver confirms it stored the file. Throws [FileException] (or
     * IOException) with a message fit for the user.
     */
    fun sendFile(to: String, meta: FileMeta, input: InputStream, progress: ((Long) -> Unit)? = null, cancelled: () -> Boolean = { false }) {
        val l = linkTo(to) ?: throw FileException("that device isn't connected right now")
        val id = Protocol.newId()
        val ack = LinkedBlockingQueue<String>()
        synchronized(lock) { fileAcks[id] = ack }
        try {
            fun msg(type: String, seq: Long, plain: ByteArray) {
                // The receiver (or the server) may have given up already.
                ack.poll()?.let { throw FileException(it.ifEmpty { "the other device stopped the transfer" }) }
                l.send(Protocol.sealFile(keys.content, Message(type, id = id, origin = me.id, originName = me.name, to = to, seq = seq), plain))
            }
            msg(Message.FILE_START, 0, meta.toJson().toString().toByteArray())
            val sum = MessageDigest.getInstance("SHA-256")
            val buf = ByteArray(Protocol.FILE_CHUNK_SIZE)
            var seq = 0L
            var sent = 0L
            while (true) {
                if (cancelled()) throw FileException("cancelled")
                var k = 0
                while (k < buf.size) {
                    val r = input.read(buf, k, buf.size - k)
                    if (r < 0) break
                    k += r
                }
                if (k > 0) {
                    sum.update(buf, 0, k)
                    msg(Message.FILE_CHUNK, seq, buf.copyOf(k))
                    seq++
                    sent += k
                    progress?.invoke(sent)
                }
                if (k < buf.size) break
            }
            if (sent != meta.size) throw FileException("the file changed while sending ($sent of ${meta.size} bytes)")
            msg(Message.FILE_END, seq, sum.digest().toHex().toByteArray())
            val e = ack.poll(fileAckTimeoutMs, TimeUnit.MILLISECONDS)
                ?: throw FileException("the other device didn't confirm the file (is VoidBridge up to date there?)")
            if (e.isNotEmpty()) throw FileException(e)
        } finally {
            synchronized(lock) { fileAcks.remove(id) }
        }
    }

    private fun sendAck(l: Link, m: Message, error: String) =
        send(l, Message(Message.FILE_ACK, id = m.id, origin = me.id, to = m.origin, error = error))

    private fun handleFile(l: Link, m: Message) {
        if (m.to != me.id) return
        if (m.type == Message.FILE_ACK) {
            synchronized(lock) { fileAcks[m.id] }?.offer(m.error)
            return
        }
        val recv = fileReceiver
        val inc = synchronized(lock) { incoming[m.id] }
        val from = FileSender(m.origin, m.originName)
        val plain = Protocol.openFile(keys.content, m) ?: return // not from our group, or tampered with

        fun fail(reason: String) {
            if (inc != null) {
                inc.w.abort()
                synchronized(lock) { incoming.remove(m.id) }
                recv?.received(m.id, inc.meta, inc.from, null, reason)
            }
            sendAck(l, m, reason)
        }

        when (m.type) {
            Message.FILE_START -> {
                if (recv == null) return sendAck(l, m, "that device can't receive files")
                val meta = try { FileMeta.fromJson(JSONObject(String(plain, Charsets.UTF_8))) } catch (_: Exception) { null }
                if (meta == null || meta.size < 0) return sendAck(l, m, "bad file header")
                val w = try {
                    recv.begin(m.id, meta, from)
                } catch (e: Exception) {
                    return sendAck(l, m, "the other device couldn't save the file: ${e.message}")
                }
                synchronized(lock) { incoming[m.id] = Incoming(meta, from, w) }
            }
            Message.FILE_CHUNK -> {
                if (inc == null) return
                if (m.seq != inc.next) return fail("file data arrived out of order")
                inc.next++
                inc.got += plain.size
                inc.active = System.currentTimeMillis()
                if (inc.got > inc.meta.size) return fail("more data than announced")
                inc.sum.update(plain)
                try {
                    inc.w.write(plain)
                } catch (e: Exception) {
                    return fail("the other device couldn't write the file: ${e.message}")
                }
                recv?.progress(m.id, inc.meta, inc.from, inc.got)
            }
            Message.FILE_END -> {
                if (inc == null) return
                synchronized(lock) { incoming.remove(m.id) }
                if (m.seq != inc.next || inc.got != inc.meta.size || String(plain, Charsets.UTF_8) != inc.sum.digest().toHex()) {
                    inc.w.abort()
                    recv?.received(m.id, inc.meta, inc.from, null, "the file arrived incomplete")
                    return sendAck(l, m, "the file arrived incomplete")
                }
                val where = try {
                    inc.w.commit()
                } catch (e: Exception) {
                    recv?.received(m.id, inc.meta, inc.from, null, e.message ?: "couldn't save the file")
                    return sendAck(l, m, "the other device couldn't save the file: ${e.message}")
                }
                recv?.received(m.id, inc.meta, inc.from, where, null)
                sendAck(l, m, "")
            }
        }
    }

    /** Drops incoming transfers that went quiet (the sender vanished). */
    private fun expireFiles() {
        val now = System.currentTimeMillis()
        val dead = synchronized(lock) {
            val d = incoming.entries.filter { now - it.value.active > fileIdleTimeoutMs }
            d.forEach { incoming.remove(it.key) }
            d
        }
        for ((id, inc) in dead) {
            inc.w.abort()
            fileReceiver?.received(id, inc.meta, inc.from, null, "the transfer stopped (connection lost)")
        }
    }

    fun links(): List<LinkInfo> = synchronized(lock) { links.map { it.info } }

    /** Every device reachable directly or through a server. */
    fun devices(): List<Device> = synchronized(lock) {
        val out = LinkedHashMap<String, Device>()
        fun add(id: String, name: String, kind: String, via: String) {
            if (id.isEmpty() || id == me.id) return
            val d = out[id]
            out[id] = if (d == null) Device(id, name, kind, listOf(via)) else if (via in d.via) d else d.copy(via = d.via + via)
        }
        for (l in links) if (l.info.kind != "server") add(l.info.peerId, l.info.name, l.info.kind, l.info.via)
        for (list in remote.values) for (p in list) add(p.id, p.name, p.kind, "Server")
        out.values.sortedBy { it.name.lowercase() }
    }

    fun shutdown() {
        sender.shutdownNow()
        timer.shutdownNow()
    }
}
