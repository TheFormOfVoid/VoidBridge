package com.theformofvoid.voidbridge.core

import org.json.JSONObject
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.Inet4Address
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.NetworkInterface
import java.net.ServerSocket
import java.net.Socket
import java.net.SocketTimeoutException
import java.util.concurrent.Executors
import java.util.concurrent.ScheduledExecutorService
import java.util.concurrent.TimeUnit

/**
 * Direct links to devices of the same group, over Wi-Fi or Tailscale; mirrors
 * internal/peer in Go. Every device listens on TCP 47829 and finds others by
 * UDP beacons, typed-in addresses, and peer exchange.
 */
class PeerManager(
    private val node: Node,
    private val keys: Keys,
    private val port: Int = Protocol.PEER_PORT,
    private val discovery: Boolean = true,
    private val log: (String) -> Unit = {},
) {
    var pingIntervalMs = 10_000L
    var idleTimeoutMs = 25_000
    var handshakeTimeoutMs = 10_000
    var dialIntervalMs = 1_000L
    /** Test hook: share and accept loopback addresses. */
    var allowLoopback = false

    private class Known(var name: String = "", var kind: String = "", val addrs: HashMap<String, Long> = HashMap())
    private class Target(var fails: Int = 0, var next: Long = 0, var dialing: Boolean = false, var manual: Boolean = false, var lastId: String = "")

    private val lock = Any()
    private val known = HashMap<String, Known>()
    private val targets = HashMap<String, Target>()
    private val links = HashMap<String, MutableSet<PeerLink>>()
    @Volatile private var running = false
    private var server: ServerSocket? = null
    private var udp: DatagramSocket? = null
    private val timers: ScheduledExecutorService = Executors.newScheduledThreadPool(2) { r -> Thread(r, "voidbridge-peers").apply { isDaemon = true } }
    private val workers = Executors.newCachedThreadPool { r -> Thread(r, "voidbridge-link").apply { isDaemon = true } }

    val listenPort: Int get() = server?.localPort ?: 0

    fun start() {
        running = true
        val ss = ServerSocket()
        ss.reuseAddress = true
        ss.bind(InetSocketAddress(port))
        server = ss
        workers.execute { acceptLoop(ss) }
        if (discovery) workers.execute { discoveryLoop() }
        timers.scheduleWithFixedDelay({ safe { dialTick() } }, 0, dialIntervalMs, TimeUnit.MILLISECONDS)
        timers.scheduleWithFixedDelay({ safe { pexTick() } }, 60, 60, TimeUnit.SECONDS)
    }

    fun stop() {
        running = false
        try { server?.close() } catch (_: Exception) {}
        udp?.close()
        timers.shutdownNow()
        synchronized(lock) { links.values.flatten() }.forEach { it.close() }
        workers.shutdownNow()
    }

    private inline fun safe(f: () -> Unit) = try { f() } catch (e: Exception) { log("peers: $e") }

    /** Addresses the user typed ("host" or "host:port"). */
    fun setManual(addrs: List<String>) = synchronized(lock) {
        targets.values.forEach { it.manual = false }
        for (a in addrs) {
            val n = normAddr(a) ?: continue
            targets.getOrPut(n) { Target() }.apply { manual = true; next = 0 }
        }
    }

    /** Retry everything now (e.g. the network changed). */
    fun kick() = synchronized(lock) { targets.values.forEach { it.next = 0; it.fails = 0 } }

    private fun normAddr(a: String): String? {
        val t = a.trim()
        if (t.isEmpty()) return null
        val i = t.lastIndexOf(':')
        return if (i > 0 && t.indexOf(':') == i) t else "$t:${Protocol.PEER_PORT}"
    }

    // ---- links ----

    private inner class PeerLink(val c: Connection) : Link {
        @Volatile private var closed = false
        override val info = LinkInfo(c.peer.id, c.peer.name, c.peer.kind, via(c.remoteHost), c.remoteAddr)
        override fun send(m: Message) = c.send(m)
        override fun close() {
            closed = true
            c.close()
        }

        fun run() {
            c.setIdleTimeout(idleTimeoutMs)
            val pinger = timers.scheduleWithFixedDelay({
                workers.execute { try { c.send(Message(Message.PING)) } catch (e: Exception) { close() } }
            }, pingIntervalMs, pingIntervalMs, TimeUnit.MILLISECONDS)
            try {
                while (!closed) {
                    val m = c.recv()
                    when (m.type) {
                        Message.PING -> c.send(Message(Message.PONG))
                        Message.PEERS -> learn(m.peers)
                        Message.CLIP -> node.handle(this, m)
                    }
                }
            } catch (_: Exception) {
            } finally {
                pinger.cancel(false)
                close()
            }
        }
    }

    private fun via(host: String): String = if (isTailscale(host)) "Tailscale" else "Wi-Fi"

    private fun isTailscale(host: String): Boolean {
        val p = host.split(".").mapNotNull { it.toIntOrNull() }
        return p.size == 4 && p[0] == 100 && p[1] in 64..127
    }

    private fun addLink(c: Connection) {
        val l = PeerLink(c)
        synchronized(lock) {
            val set = links.getOrPut(c.peer.id) { HashSet() }
            if (c.dialer && set.isNotEmpty()) {
                c.close()
                return
            }
            set.add(l)
            val k = known.getOrPut(c.peer.id) { Known() }
            k.name = c.peer.name
            k.kind = c.peer.kind
            val now = System.currentTimeMillis()
            c.peerAddrs.forEach { k.addrs[it] = now }
        }
        log("linked with ${c.peer.name} (${c.remoteAddr}) via ${l.info.via}")
        node.addLink(l)
        workers.execute { try { l.send(Message(Message.PEERS, peers = peerList())) } catch (_: Exception) {} }
        l.run()
        synchronized(lock) {
            links[c.peer.id]?.let { it.remove(l); if (it.isEmpty()) links.remove(c.peer.id) }
        }
        node.removeLink(l)
        log("unlinked ${c.peer.name}")
    }

    private fun acceptLoop(ss: ServerSocket) {
        while (running) {
            val s = try { ss.accept() } catch (e: Exception) { if (!running) return; Thread.sleep(200); continue }
            workers.execute {
                try {
                    tune(s)
                    addLink(Connection.handshake(s, keys.link, node.me, myAddrs(), false, handshakeTimeoutMs))
                } catch (e: Exception) {
                    try { s.close() } catch (_: Exception) {}
                }
            }
        }
    }

    private fun tune(s: Socket) {
        s.tcpNoDelay = true
        s.keepAlive = true
    }

    // ---- knowledge ----

    private fun learn(peers: List<PeerInfo>) = synchronized(lock) {
        val now = System.currentTimeMillis()
        for (p in peers) {
            if (p.id.isEmpty() || p.id == node.me.id) continue
            val k = known.getOrPut(p.id) { Known() }
            if (p.name.isNotEmpty()) { k.name = p.name; k.kind = p.kind }
            for (a in p.addrs.take(8)) {
                val host = a.substringBeforeLast(':')
                if (host.isEmpty() || host == "0.0.0.0" || (!allowLoopback && host.startsWith("127."))) continue
                k.addrs[a] = now
            }
        }
    }

    private fun heard(id: String, name: String, kind: String, addr: String) = synchronized(lock) {
        val k = known.getOrPut(id) { Known() }
        k.name = name
        k.kind = kind
        if (addr !in k.addrs) targets.getOrPut(addr) { Target() }.next = 0
        k.addrs[addr] = System.currentTimeMillis()
    }

    private fun peerList(): List<PeerInfo> {
        val out = mutableListOf(PeerInfo(node.me.id, node.me.name, node.me.kind, myAddrs()))
        val cutoff = System.currentTimeMillis() - FORGET_AFTER
        synchronized(lock) {
            for ((id, k) in known) {
                val addrs = k.addrs.filterValues { it > cutoff }.keys.sorted()
                if (addrs.isNotEmpty()) out.add(PeerInfo(id, k.name, k.kind, addrs))
            }
        }
        return out
    }

    private fun pexTick() {
        val all = synchronized(lock) { links.values.flatten() }
        val msg = Message(Message.PEERS, peers = peerList())
        all.forEach { l -> workers.execute { try { l.send(msg) } catch (_: Exception) {} } }
    }

    // ---- dialing ----

    private fun dialTick() {
        val now = System.currentTimeMillis()
        val picks = mutableListOf<String>()
        synchronized(lock) {
            fun consider(addr: String) {
                val t = targets.getOrPut(addr) { Target() }
                if (t.dialing || now < t.next) return
                if (t.lastId.isNotEmpty() && !links[t.lastId].isNullOrEmpty()) return
                t.dialing = true
                picks.add(addr)
            }
            for ((id, k) in known) {
                if (!links[id].isNullOrEmpty()) continue
                k.addrs.entries.removeIf { now - it.value > FORGET_AFTER }
                k.addrs.keys.toList().forEach { consider(it) }
            }
            targets.filter { it.value.manual }.keys.forEach { consider(it) }
        }
        picks.forEach { a -> workers.execute { dial(a) } }
    }

    private fun dial(addr: String) {
        val host = addr.substringBeforeLast(':')
        val p = addr.substringAfterLast(':').toIntOrNull() ?: Protocol.PEER_PORT
        var conn: Connection? = null
        val s = Socket()
        try {
            tune(s)
            s.connect(InetSocketAddress(host, p), 3_000)
            conn = Connection.handshake(s, keys.link, node.me, myAddrs(), true, handshakeTimeoutMs)
        } catch (e: Exception) {
            try { s.close() } catch (_: Exception) {}
        }
        synchronized(lock) {
            val t = targets.getOrPut(addr) { Target() }
            t.dialing = false
            if (conn == null) {
                t.fails++
                t.next = System.currentTimeMillis() + (1000L shl minOf(t.fails, 6))
                return
            }
            t.fails = 0
            t.lastId = conn.peer.id
            t.next = System.currentTimeMillis() + 2_000
        }
        if (!running) { conn?.close(); return }
        addLink(conn!!)
    }

    // ---- addresses & discovery ----

    fun myAddrs(): List<String> {
        val p = listenPort.takeIf { it != 0 } ?: port
        val out = mutableListOf<String>()
        if (allowLoopback) out.add("127.0.0.1:$p")
        localIPs().forEach { out.add("$it:$p") }
        return out
    }

    private fun discoveryLoop() {
        val fp = keys.fingerprint
        while (running) {
            try {
                DatagramSocket(null).use { s ->
                    s.reuseAddress = true
                    s.broadcast = true
                    s.soTimeout = 3_000
                    s.bind(InetSocketAddress(Protocol.DISCOVERY_PORT))
                    udp = s
                    val me = JSONObject().put("app", "voidbridge").put("v", Protocol.VERSION).put("id", node.me.id)
                        .put("name", node.me.name).put("kind", node.me.kind).put("port", listenPort).put("fp", fp).toString().toByteArray()
                    val probe = JSONObject().put("app", "voidbridge").put("v", Protocol.VERSION).put("type", "probe").toString().toByteArray()
                    broadcast(s, probe)
                    var lastBeacon = 0L
                    val buf = ByteArray(2048)
                    while (running) {
                        if (System.currentTimeMillis() - lastBeacon > 3_000) {
                            broadcast(s, me)
                            lastBeacon = System.currentTimeMillis()
                        }
                        val pkt = DatagramPacket(buf, buf.size)
                        try { s.receive(pkt) } catch (_: SocketTimeoutException) { continue }
                        val o = try { JSONObject(String(pkt.data, 0, pkt.length, Charsets.UTF_8)) } catch (_: Exception) { continue }
                        if (o.optString("app") != "voidbridge") continue
                        if (o.optString("type") == "probe") {
                            try { s.send(DatagramPacket(me, me.size, pkt.address, pkt.port)) } catch (_: Exception) {}
                            continue
                        }
                        val id = o.optString("id")
                        val bport = o.optInt("port")
                        if (o.optString("fp") != fp || id.isEmpty() || id == node.me.id || bport <= 0) continue
                        heard(id, o.optString("name"), o.optString("kind"), "${pkt.address.hostAddress}:$bport")
                    }
                }
            } catch (e: Exception) {
                if (!running) return
                log("discovery: $e")
                try { Thread.sleep(2_000) } catch (_: InterruptedException) { return }
            }
        }
    }

    private fun broadcast(s: DatagramSocket, payload: ByteArray) {
        val dsts = mutableSetOf<InetAddress>(InetAddress.getByName("255.255.255.255"))
        try {
            for (ni in NetworkInterface.getNetworkInterfaces()) {
                if (!ni.isUp || ni.isLoopback) continue
                ni.interfaceAddresses.mapNotNullTo(dsts) { it.broadcast }
            }
        } catch (_: Exception) {}
        for (d in dsts) try { s.send(DatagramPacket(payload, payload.size, d, Protocol.DISCOVERY_PORT)) } catch (_: Exception) {}
    }

    companion object {
        const val FORGET_AFTER = 30 * 60 * 1000L

        /** Private-LAN and Tailscale IPv4 addresses of this device. */
        fun localIPs(): List<String> {
            val out = mutableListOf<String>()
            try {
                for (ni in NetworkInterface.getNetworkInterfaces()) {
                    if (!ni.isUp || ni.isLoopback) continue
                    for (a in ni.inetAddresses) {
                        if (a !is Inet4Address || a.isLoopbackAddress || a.isLinkLocalAddress) continue
                        val b = a.address.map { it.toInt() and 0xff }
                        val private = b[0] == 10 || (b[0] == 172 && b[1] in 16..31) || (b[0] == 192 && b[1] == 168)
                        val tailscale = b[0] == 100 && b[1] in 64..127
                        if (private || tailscale) out.add(a.hostAddress)
                    }
                }
            } catch (_: Exception) {}
            return out
        }
    }
}
