package com.theformofvoid.voidbridge.core

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
 * The VoidBridge wire protocol. Must stay byte-compatible with
 * pc/internal/protocol; docs/PROTOCOL.md is the reference.
 */
object Protocol {
    const val VERSION = 1
    const val MAX_FRAME = 4 shl 20
    const val MAX_TEXT = 1 shl 20
    const val DEFAULT_PORT = 47829
    const val DISCOVERY_PORT = 47830

    private const val PAIRING_ITERATIONS = 200_000
    private const val PAIRING_SALT = "voidbridge-pairing-v1"
    private const val SESSION_LABEL = "voidbridge-session-v1"
    private const val BEACON_LABEL = "voidbridge-beacon-v1"
    const val NONCE_SIZE = 16

    const val DIR_CLIENT = 0x76620001
    const val DIR_SERVER = 0x76620002

    private val random = SecureRandom()

    /** Same rules as the PC: uppercase, drop separators, fix 0/1/8 typos. */
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

    /** Slow on purpose (PBKDF2); call off the main thread. */
    fun deriveKey(code: String): ByteArray {
        val spec = PBEKeySpec(normalizeCode(code).toCharArray(), PAIRING_SALT.toByteArray(), PAIRING_ITERATIONS, 256)
        return SecretKeyFactory.getInstance("PBKDF2WithHmacSHA256").generateSecret(spec).encoded
    }

    fun sessionKey(pairingKey: ByteArray, clientNonce: ByteArray, serverNonce: ByteArray): ByteArray =
        hmac(pairingKey, SESSION_LABEL.toByteArray(), clientNonce, serverNonce)

    fun fingerprint(pairingKey: ByteArray): String =
        hmac(pairingKey, BEACON_LABEL.toByteArray()).copyOf(8).toHex()

    fun newNonce(): ByteArray = ByteArray(NONCE_SIZE).also { random.nextBytes(it) }

    fun newId(): String = ByteArray(8).also { random.nextBytes(it) }.toHex()

    fun sha256(s: String): String = MessageDigest.getInstance("SHA-256").digest(s.toByteArray()).toHex()

    private fun hmac(key: ByteArray, vararg parts: ByteArray): ByteArray {
        val mac = Mac.getInstance("HmacSHA256")
        mac.init(SecretKeySpec(key, "HmacSHA256"))
        parts.forEach { mac.update(it) }
        return mac.doFinal()
    }
}

fun ByteArray.toHex(): String = joinToString("") { "%02x".format(it) }

fun String.hexToBytes(): ByteArray = ByteArray(length / 2) { substring(it * 2, it * 2 + 2).toInt(16).toByte() }

class AuthException : IOException("authentication failed (wrong pairing code?)")

/** AES-256-GCM with direction-tagged counter nonces; see protocol.Cipher in Go. */
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
        val plain = try {
            c.doFinal(sealed)
        } catch (e: AEADBadTagException) {
            throw AuthException()
        }
        recvCount++
        return plain
    }
}

/** One protocol message. Field names match the Go JSON tags. */
data class Message(
    val type: String,
    val version: Int = 0,
    val id: String = "",
    val name: String = "",
    val nonce: ByteArray? = null,
    val time: Long = 0,
    val text: String = "",
) {
    fun encode(): ByteArray {
        val o = JSONObject().put("t", type)
        if (version != 0) o.put("v", version)
        if (id.isNotEmpty()) o.put("id", id)
        if (name.isNotEmpty()) o.put("name", name)
        if (nonce != null) o.put("nonce", Base64.getEncoder().encodeToString(nonce))
        if (time != 0L) o.put("time", time)
        if (text.isNotEmpty()) o.put("text", text)
        return o.toString().toByteArray(Charsets.UTF_8)
    }

    companion object {
        const val HELLO = "hello"
        const val READY = "ready"
        const val PING = "ping"
        const val PONG = "pong"
        const val CLIP = "clip"
        const val ACK = "ack"

        fun decode(b: ByteArray): Message {
            val o = try {
                JSONObject(String(b, Charsets.UTF_8))
            } catch (e: Exception) {
                throw IOException("bad message", e)
            }
            val t = o.optString("t")
            if (t.isEmpty()) throw IOException("message without type")
            return Message(
                type = t,
                version = o.optInt("v"),
                id = o.optString("id"),
                name = o.optString("name"),
                nonce = o.optString("nonce").takeIf { it.isNotEmpty() }?.let { Base64.getDecoder().decode(it) },
                time = o.optLong("time"),
                text = o.optString("text"),
            )
        }
    }
}

/** An authenticated, encrypted connection (client side). */
class Connection private constructor(
    private val socket: Socket,
    private val input: DataInputStream,
    private val output: DataOutputStream,
    private val cipher: FrameCipher,
    val peerId: String,
    val peerName: String,
    /** Peer clock minus our clock, in ms. */
    val clockOffset: Long,
) {
    fun send(m: Message) = synchronized(output) {
        writeFrame(output, cipher.seal(m.encode()))
    }

    /** Only one thread may receive. Throws SocketTimeoutException on read timeout. */
    fun recv(): Message = Message.decode(cipher.open(readFrame(input)))

    fun close() = try {
        socket.close()
    } catch (_: IOException) {
    }

    companion object {
        fun writeFrame(out: DataOutputStream, payload: ByteArray) {
            if (payload.size > Protocol.MAX_FRAME) throw IOException("frame too large")
            out.writeInt(payload.size)
            out.write(payload)
            out.flush()
        }

        fun readFrame(input: DataInputStream): ByteArray {
            val n = input.readInt()
            if (n < 0 || n > Protocol.MAX_FRAME) throw IOException("frame too large")
            return ByteArray(n).also { input.readFully(it) }
        }

        /** Runs the client handshake over a connected socket. */
        fun handshake(socket: Socket, pairingKey: ByteArray, myId: String, myName: String, timeoutMs: Int): Connection {
            val oldTimeout = socket.soTimeout
            socket.soTimeout = timeoutMs
            val input = DataInputStream(socket.getInputStream().buffered())
            val output = DataOutputStream(socket.getOutputStream().buffered())

            val myNonce = Protocol.newNonce()
            writeFrame(output, Message(Message.HELLO, version = Protocol.VERSION, id = myId, name = myName,
                nonce = myNonce, time = System.currentTimeMillis()).encode())
            val peer = Message.decode(readFrame(input))
            if (peer.type != Message.HELLO) throw IOException("expected hello, got ${peer.type}")
            if (peer.version != Protocol.VERSION) throw IOException("PC speaks protocol ${peer.version}, we speak ${Protocol.VERSION}; update both apps")
            val serverNonce = peer.nonce ?: throw IOException("hello without nonce")
            if (serverNonce.size != Protocol.NONCE_SIZE) throw IOException("bad hello nonce")

            val cipher = FrameCipher(Protocol.sessionKey(pairingKey, myNonce, serverNonce), isServer = false)
            val offset = if (peer.time != 0L) peer.time - System.currentTimeMillis() else 0L
            val conn = Connection(socket, input, output, cipher, peer.id, peer.name, offset)
            conn.send(Message(Message.READY))
            val ready = conn.recv()
            if (ready.type != Message.READY) throw IOException("expected ready, got ${ready.type}")
            socket.soTimeout = oldTimeout
            return conn
        }
    }
}
