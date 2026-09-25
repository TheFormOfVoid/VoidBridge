package com.theformofvoid.voidbridge.core

import org.json.JSONArray
import org.json.JSONObject
import java.io.DataInputStream
import java.io.DataOutputStream
import java.io.IOException
import java.net.Socket
import java.nio.ByteBuffer
import java.security.MessageDigest
import java.security.SecureRandom
import java.util.Base64
import javax.crypto.AEADBadTagException
import javax.crypto.Cipher
import javax.crypto.Mac
import javax.crypto.SecretKeyFactory
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.PBEKeySpec
import javax.crypto.spec.SecretKeySpec

/**
 * The VoidBridge wire protocol, version 2. Byte-compatible with the Go code in
 * internal/protocol; docs/PROTOCOL.md is the reference.
 */
object Protocol {
    const val VERSION = 2
    const val MAX_FRAME = 40 shl 20
    const val MAX_TEXT = 1 shl 20
    const val MAX_IMAGE = 25 shl 20
    const val PEER_PORT = 47829
    const val DISCOVERY_PORT = 47830
    const val SERVER_PORT = 47831
    const val NONCE_SIZE = 16
    private const val PBKDF_ITERATIONS = 200_000

    const val DIR_CLIENT = 0x76620001
    const val DIR_SERVER = 0x76620002

    const val CLIP_TEXT = "text"
    const val CLIP_IMAGE = "image"

    private val random = SecureRandom()

    fun normalizeCode(code: String): String = buildString {
        for (c in code.uppercase()) {
            when (c) {
                '0' -> append('O')
                '1' -> append('I')
                '8' -> append('B')
                in 'A'..'Z', in '2'..'7' -> append(c)
            }
        }
    }

    fun validCode(code: String) = normalizeCode(code).length == 16

    fun formatCode(code: String): String {
        val n = normalizeCode(code)
        return if (n.length != 16) code else n.chunked(4).joinToString("-")
    }

    fun newGroupCode(): String {
        val alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"
        return formatCode(String(CharArray(16) { alphabet[random.nextInt(32)] }))
    }

    fun normalizeUsername(u: String) = u.trim().lowercase()

    private fun pbkdf(secret: String, salt: String): ByteArray {
        val spec = PBEKeySpec(secret.toCharArray(), salt.toByteArray(), PBKDF_ITERATIONS, 256)
        return SecretKeyFactory.getInstance("PBKDF2WithHmacSHA256").generateSecret(spec).encoded
    }

    /** Slow on purpose; call off the main thread. */
    fun masterFromCode(code: String) = pbkdf(normalizeCode(code), "voidbridge-pairing-v1")

    /** Slow on purpose; call off the main thread. */
    fun masterFromAccount(username: String, password: String) =
        pbkdf(password, "voidbridge-account-v1:" + normalizeUsername(username))

    fun hmac(key: ByteArray, vararg parts: ByteArray): ByteArray {
        val mac = Mac.getInstance("HmacSHA256")
        mac.init(SecretKeySpec(key, "HmacSHA256"))
        parts.forEach { mac.update(it) }
        return mac.doFinal()
    }

    /** HKDF-SHA256 (RFC 5869) with an empty salt, for one 32-byte output. */
    fun hkdf(secret: ByteArray, info: String): ByteArray {
        val prk = hmac(ByteArray(32), secret)
        return hmac(prk, info.toByteArray(), byteArrayOf(1))
    }

    fun sessionKey(linkKey: ByteArray, clientNonce: ByteArray, serverNonce: ByteArray) =
        hmac(linkKey, "voidbridge-session-v2".toByteArray(), clientNonce, serverNonce)

    fun newNonce(): ByteArray = ByteArray(NONCE_SIZE).also { random.nextBytes(it) }
    fun newId(): String = ByteArray(8).also { random.nextBytes(it) }.toHex()

    fun hash(kind: String, data: ByteArray): String {
        val md = MessageDigest.getInstance("SHA-256")
        md.update(kind.toByteArray())
        md.update(0)
        md.update(data)
        return md.digest().toHex()
    }

    // ---- end-to-end clip encryption ----

    private fun clipAAD(m: Message) =
        "voidbridge-clip-v2|${m.id}|${m.origin}|${m.time}|${m.clipType}|${m.mime}".toByteArray()

    fun sealClip(contentKey: ByteArray, m: Message, plaintext: ByteArray): Message {
        val nonce = ByteArray(12).also { random.nextBytes(it) }
        val c = Cipher.getInstance("AES/GCM/NoPadding")
        c.init(Cipher.ENCRYPT_MODE, SecretKeySpec(contentKey, "AES"), GCMParameterSpec(128, nonce))
        c.updateAAD(clipAAD(m))
        return m.copy(body = nonce + c.doFinal(plaintext))
    }

    fun openClip(contentKey: ByteArray, m: Message): ByteArray? {
        val body = m.body ?: return null
        if (body.size < 12 + 16) return null
        return try {
            val c = Cipher.getInstance("AES/GCM/NoPadding")
            c.init(Cipher.DECRYPT_MODE, SecretKeySpec(contentKey, "AES"), GCMParameterSpec(128, body, 0, 12))
            c.updateAAD(clipAAD(m))
            c.doFinal(body, 12, body.size - 12)
        } catch (e: Exception) {
            null
        }
    }
}

fun ByteArray.toHex(): String = joinToString("") { "%02x".format(it) }
fun String.hexToBytes(): ByteArray = ByteArray(length / 2) { substring(it * 2, it * 2 + 2).toInt(16).toByte() }

/** Everything derived from a group secret. */
class Keys(val master: ByteArray) {
    val link = Protocol.hkdf(master, "voidbridge-link-v2")
    val content = Protocol.hkdf(master, "voidbridge-content-v2")
    val auth = Protocol.hkdf(master, "voidbridge-auth-v2")
    val fingerprint: String = Protocol.hmac(link, "voidbridge-beacon-v2".toByteArray()).copyOf(8).toHex()
}

data class Identity(val id: String, val name: String, val kind: String = "android")

data class PeerInfo(val id: String, val name: String = "", val kind: String = "", val addrs: List<String> = emptyList()) {
    fun toJson(): JSONObject = JSONObject().put("id", id).apply {
        if (name.isNotEmpty()) put("name", name)
        if (kind.isNotEmpty()) put("kind", kind)
        if (addrs.isNotEmpty()) put("addrs", JSONArray(addrs))
    }

    companion object {
        fun fromJson(o: JSONObject) = PeerInfo(
            o.optString("id"), o.optString("name"), o.optString("kind"),
            o.optJSONArray("addrs")?.let { a -> List(a.length()) { a.optString(it) } } ?: emptyList(),
        )
    }
}

/** A frame header plus optional binary body. Field names match the Go JSON tags. */
data class Message(
    val type: String,
    val version: Int = 0,
    val id: String = "",
    val name: String = "",
    val kind: String = "",
    val nonce: ByteArray? = null,
    val addrs: List<String> = emptyList(),
    val origin: String = "",
    val originName: String = "",
    val time: Long = 0,
    val clipType: String = "",
    val mime: String = "",
    val peers: List<PeerInfo> = emptyList(),
    val body: ByteArray? = null,
) {
    /** Newer by copy time, then id, exactly as the Go side orders clips. */
    fun newerThan(cur: Message?): Boolean =
        cur == null || time > cur.time || (time == cur.time && id > cur.id)

    fun encode(): ByteArray {
        val o = JSONObject().put("t", type)
        if (version != 0) o.put("v", version)
        if (id.isNotEmpty()) o.put("id", id)
        if (name.isNotEmpty()) o.put("name", name)
        if (kind.isNotEmpty()) o.put("kind", kind)
        if (nonce != null) o.put("nonce", Base64.getEncoder().encodeToString(nonce))
        if (addrs.isNotEmpty()) o.put("addrs", JSONArray(addrs))
        if (origin.isNotEmpty()) o.put("origin", origin)
        if (originName.isNotEmpty()) o.put("origin_name", originName)
        if (time != 0L) o.put("time", time)
        if (clipType.isNotEmpty()) o.put("ctype", clipType)
        if (mime.isNotEmpty()) o.put("mime", mime)
        if (peers.isNotEmpty()) o.put("peers", JSONArray(peers.map { it.toJson() }))
        val h = o.toString().toByteArray(Charsets.UTF_8)
        val b = body ?: ByteArray(0)
        return ByteBuffer.allocate(4 + h.size + b.size).putInt(h.size).put(h).put(b).array()
    }

    companion object {
        const val HELLO = "hello"
        const val READY = "ready"
        const val PING = "ping"
        const val PONG = "pong"
        const val CLIP = "clip"
        const val PEERS = "peers"
        const val DEVICES = "devices"

        fun decode(b: ByteArray): Message {
            if (b.size < 4) throw IOException("short frame")
            val n = ByteBuffer.wrap(b).int
            if (n < 0 || n > b.size - 4) throw IOException("bad header length")
            val o = try {
                JSONObject(String(b, 4, n, Charsets.UTF_8))
            } catch (e: Exception) {
                throw IOException("bad header", e)
            }
            val t = o.optString("t")
            if (t.isEmpty()) throw IOException("message without type")
            fun strings(key: String) = o.optJSONArray(key)?.let { a -> List(a.length()) { a.optString(it) } } ?: emptyList()
            return Message(
                type = t,
                version = o.optInt("v"),
                id = o.optString("id"),
                name = o.optString("name"),
                kind = o.optString("kind"),
                nonce = o.optString("nonce").takeIf { it.isNotEmpty() }?.let { Base64.getDecoder().decode(it) },
                addrs = strings("addrs"),
                origin = o.optString("origin"),
                originName = o.optString("origin_name"),
                time = o.optLong("time"),
                clipType = o.optString("ctype"),
                mime = o.optString("mime"),
                peers = o.optJSONArray("peers")?.let { a -> List(a.length()) { PeerInfo.fromJson(a.getJSONObject(it)) } } ?: emptyList(),
                body = if (b.size > 4 + n) b.copyOfRange(4 + n, b.size) else null,
            )
        }
    }
}

class AuthException : IOException("devices are not in the same group")

/** AES-256-GCM with direction-tagged counter nonces (direct links only). */
class FrameCipher(sessionKey: ByteArray, isServer: Boolean) {
    private val key = SecretKeySpec(sessionKey, "AES")
    private val sendDir = if (isServer) Protocol.DIR_SERVER else Protocol.DIR_CLIENT
    private val recvDir = if (isServer) Protocol.DIR_CLIENT else Protocol.DIR_SERVER
    private var sendCount = 0L
    private var recvCount = 0L

    private fun nonce(dir: Int, n: Long) = ByteBuffer.allocate(12).putInt(dir).putLong(n).array()

    fun seal(plain: ByteArray): ByteArray {
        val c = Cipher.getInstance("AES/GCM/NoPadding")
        c.init(Cipher.ENCRYPT_MODE, key, GCMParameterSpec(128, nonce(sendDir, sendCount++)))
        return c.doFinal(plain)
    }

    fun open(sealed: ByteArray): ByteArray {
        val c = Cipher.getInstance("AES/GCM/NoPadding")
        c.init(Cipher.DECRYPT_MODE, key, GCMParameterSpec(128, nonce(recvDir, recvCount)))
        val p = try {
            c.doFinal(sealed)
        } catch (e: AEADBadTagException) {
            throw AuthException()
        }
        recvCount++
        return p
    }
}

/** An authenticated, encrypted direct link to another device. */
class Connection private constructor(
    private val socket: Socket,
    private val input: DataInputStream,
    private val output: DataOutputStream,
    private val cipher: FrameCipher,
    val peer: Identity,
    val peerAddrs: List<String>,
    val dialer: Boolean,
) {
    private val sendLock = Any()
    private val recvLock = Any()

    val remoteHost: String get() = socket.inetAddress?.hostAddress ?: ""
    val remoteAddr: String get() = "$remoteHost:${socket.port}"

    fun send(m: Message) {
        val plain = m.encode()
        synchronized(sendLock) { writeFrame(output, cipher.seal(plain)) }
    }

    /** Blocks until a message arrives; throws SocketTimeoutException after the socket's soTimeout of silence. */
    fun recv(): Message = synchronized(recvLock) { Message.decode(cipher.open(readFrame(input))) }

    fun setIdleTimeout(ms: Int) {
        socket.soTimeout = ms
    }

    fun close() = try {
        socket.close()
    } catch (_: IOException) {
    }

    companion object {
        fun writeFrame(out: DataOutputStream, payload: ByteArray) {
            if (payload.size > Protocol.MAX_FRAME + 64) throw IOException("frame too large")
            out.writeInt(payload.size)
            out.write(payload)
            out.flush()
        }

        fun readFrame(input: DataInputStream, max: Int = Protocol.MAX_FRAME + 64): ByteArray {
            val n = input.readInt()
            if (n < 0 || n > max) throw IOException("frame too large")
            return ByteArray(n).also { input.readFully(it) }
        }

        fun handshake(socket: Socket, linkKey: ByteArray, me: Identity, myAddrs: List<String>, dialer: Boolean, timeoutMs: Int): Connection {
            socket.soTimeout = timeoutMs
            val input = DataInputStream(socket.getInputStream().buffered(64 * 1024))
            val output = DataOutputStream(socket.getOutputStream().buffered(64 * 1024))
            val myNonce = Protocol.newNonce()
            writeFrame(output, Message(Message.HELLO, version = Protocol.VERSION, id = me.id, name = me.name, kind = me.kind, nonce = myNonce, addrs = myAddrs).encode())
            val peer = Message.decode(readFrame(input, 64 * 1024))
            if (peer.type != Message.HELLO) throw IOException("expected hello, got ${peer.type}")
            if (peer.version != Protocol.VERSION) throw IOException("the other device speaks protocol ${peer.version}; update VoidBridge on both")
            val peerNonce = peer.nonce ?: throw IOException("hello without nonce")
            if (peerNonce.size != Protocol.NONCE_SIZE || peer.id.isEmpty()) throw IOException("bad hello")
            if (peer.id == me.id) throw IOException("connected to myself")
            val (cn, sn) = if (dialer) myNonce to peerNonce else peerNonce to myNonce
            val conn = Connection(
                socket, input, output, FrameCipher(Protocol.sessionKey(linkKey, cn, sn), !dialer),
                Identity(peer.id, peer.name, peer.kind), peer.addrs, dialer,
            )
            conn.send(Message(Message.READY))
            val ready = conn.recv()
            if (ready.type != Message.READY) throw IOException("expected ready, got ${ready.type}")
            return conn
        }
    }
}
