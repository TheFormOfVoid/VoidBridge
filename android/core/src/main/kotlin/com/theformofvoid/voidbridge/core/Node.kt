package com.theformofvoid.voidbridge.core

import java.util.concurrent.Executors

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
    }
}
