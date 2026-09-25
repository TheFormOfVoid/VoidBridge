package com.theformofvoid.voidbridge.core

import java.net.InetSocketAddress
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit
import kotlin.test.Test
import kotlin.test.assertContentEquals
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertNotNull

class ProtocolTest {
    // Same vectors as TestKnownAnswers in pc/internal/protocol.
    @Test
    fun knownAnswersMatchGo() {
        val key = Protocol.deriveKey("ABCD-EFGH-IJKL-MNOP")
        assertEquals("9e3f6a5f684ceef721f4e5f99e8e61f435dc7f28aaa0c4658803808be73c7d59", key.toHex())
        val sk = Protocol.sessionKey(key, ByteArray(16) { 1 }, ByteArray(16) { 2 })
        assertEquals("01e00776fc64f1cfc049e5a73b30e7a48ea86a0ee2e32cb7d3a87fe69c50a7c0", sk.toHex())
        val frame = FrameCipher(sk, isServer = false).seal("""{"t":"ready"}""".toByteArray())
        assertEquals("093dfe1177152db81e25146905bbcdf7b6e3d8926c36d21e13b6d67d3e", frame.toHex())
    }

    @Test
    fun normalize() {
        assertEquals("ABCDEFOI234567B", Protocol.normalizeCode("abcd-ef01-2345-6789"))
    }

    @Test
    fun cipherRejectsReplayAndReflection() {
        val sk = Protocol.sessionKey(ByteArray(32), Protocol.newNonce(), Protocol.newNonce())
        val cli = FrameCipher(sk, isServer = false)
        val srv = FrameCipher(sk, isServer = true)
        val f = cli.seal("one".toByteArray())
        assertContentEquals("one".toByteArray(), srv.open(f))
        assertFailsWith<AuthException> { srv.open(f) }
        assertFailsWith<AuthException> { srv.open(srv.seal("x".toByteArray())) }
    }

    @Test
    fun messageRoundTrip() {
        val m = Message(Message.CLIP, id = "abc", time = 1234567890123, text = "héllo \"👋\"\n", nonce = byteArrayOf(1, 2, 3))
        val d = Message.decode(m.encode())
        assertEquals(m.text, d.text)
        assertEquals(m.time, d.time)
        assertContentEquals(m.nonce, d.nonce)
    }

    /**
     * End-to-end against the real Go PC app (pc/cmd/voidbridge -headless).
     * Skipped unless VOIDBRIDGE_PC_ADDR and VOIDBRIDGE_PC_CODE are set.
     */
    @Test
    fun interopWithGoServer() {
        val addr = System.getenv("VOIDBRIDGE_PC_ADDR") ?: return
        val code = System.getenv("VOIDBRIDGE_PC_CODE") ?: return
        val (host, port) = addr.split(":")
        val received = LinkedBlockingQueue<String>()
        val statuses = LinkedBlockingQueue<SyncClient.Status>()
        val client = SyncClient(
            Protocol.deriveKey(code), "test-phone", "Test Phone",
            { listOf(InetSocketAddress(host, port.toInt())) },
            object : SyncClient.Listener {
                override fun applyRemoteClip(text: String) { received.put(text) }
                override fun onStatus(status: SyncClient.Status) { statuses.put(status) }
                override fun log(msg: String) = println(msg)
            },
        )
        client.start()
        try {
            while (true) {
                val s = assertNotNull(statuses.poll(10, TimeUnit.SECONDS), "never connected")
                if (s is SyncClient.Status.Connected) break
            }
            // The Go side runs with a memory clipboard in headless test mode and
            // echoes nothing back; a successful handshake plus a clean send is the check.
            client.localClip("hello from kotlin ✓")
            Thread.sleep(500)
            assert(client.isConnected) { "connection dropped after sending a clip" }
        } finally {
            client.stop()
        }
    }
}
