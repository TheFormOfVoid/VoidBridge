package com.theformofvoid.voidbridge.core

import java.util.concurrent.CopyOnWriteArrayList
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertContentEquals
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertNotNull
import kotlin.test.assertNull
import kotlin.test.assertTrue
import kotlin.test.fail

class ProtocolTest {
    // Same vectors as TestKnownAnswers in internal/protocol (Go).
    @Test
    fun knownAnswersMatchGo() {
        val code = Keys(Protocol.masterFromCode("ABCD-EFGH-IJKL-MNOP"))
        assertEquals("9e3f6a5f684ceef721f4e5f99e8e61f435dc7f28aaa0c4658803808be73c7d59", code.master.toHex())
        val acct = Keys(Protocol.masterFromAccount(" Alice ", "correct horse"))
        assertEquals("02de1a5fe7b5eaaac9fea0798fd73520a99acb964edfecc937f4433ff5a6715a", acct.master.toHex())
        assertEquals("7115bab6f55f9e7149e3eedae05a2f4cbb3977d0b1e2ca52306f821e73b96c2b", code.link.toHex())
        assertEquals("3baec9577d1694f354d68114a6fa44fe4cfabfdddede1548844c58018e6ce2e3", code.content.toHex())
        assertEquals("5d880d9c8233fb24a0819091fdc4fb1cde7878be0f4a8cebb774d55f8961615e", acct.auth.toHex())
        assertEquals("96001ca5c8506c14", code.fingerprint)
        val sk = Protocol.sessionKey(code.link, ByteArray(16) { 1 }, ByteArray(16) { 2 })
        assertEquals("7b364ea83e6e69c4ceb6c712569e0614f6628a08ae1c4d8f8bd88871599ea039", sk.toHex())
        assertEquals("57a9fc60d3c90b7001a2379a1334f0ccd51dbce163248160116646cfcf", FrameCipher(sk, false).seal("""{"t":"ready"}""".toByteArray()).toHex())
        assertEquals("0000000c7b2274223a2270696e67227d09", Message(Message.PING, body = byteArrayOf(9)).encode().toHex())
    }

    @Test
    fun codes() {
        assertEquals("ABCDEFOI234567B", Protocol.normalizeCode("abcd-ef01-2345-6789"))
        assertTrue(Protocol.validCode(Protocol.newGroupCode()))
        assertEquals("ABCD-EFGH-IJKL-MNOP", Protocol.formatCode("abcdefghijklmnop"))
    }

    @Test
    fun clipSealing() {
        val k = Keys(Protocol.masterFromCode("AAAA-BBBB-CCCC-DDDD"))
        val m = Protocol.sealClip(k.content, Message(Message.CLIP, id = "x", origin = "o", time = 5, clipType = "text", mime = "text/plain"), "secret".toByteArray())
        assertContentEquals("secret".toByteArray(), Protocol.openClip(k.content, m))
        assertNull(Protocol.openClip(k.content, m.copy(time = 6)), "tampered header accepted")
        val d = Message.decode(m.encode())
        assertContentEquals(m.body, d.body)
        assertEquals("o", d.origin)
    }

    @Test
    fun cipherRejectsReplay() {
        val sk = Protocol.sessionKey(ByteArray(32), Protocol.newNonce(), Protocol.newNonce())
        val cli = FrameCipher(sk, false)
        val srv = FrameCipher(sk, true)
        val f = cli.seal("one".toByteArray())
        assertContentEquals("one".toByteArray(), srv.open(f))
        assertFailsWith<AuthException> { srv.open(f) }
    }

    // ---- Kotlin devices talking to each other ----

    class Dev(name: String, keys: Keys) : Node.Listener {
        val got = CopyOnWriteArrayList<Content>()
        val node = Node(Identity("kt-$name", name), keys, this)
        val peers = PeerManager(node, keys, port = 0, discovery = false).apply {
            allowLoopback = true
            dialIntervalMs = 50
            pingIntervalMs = 200
            idleTimeoutMs = 2_000
            start()
        }
        val addr get() = "127.0.0.1:${peers.listenPort}"
        override fun apply(content: Content, from: Message) { got.add(content) }
        fun lastText() = got.lastOrNull()?.text
        fun stop() { peers.stop(); node.shutdown() }
    }

    private val cleanup = mutableListOf<() -> Unit>()

    @AfterTest
    fun tearDown() = cleanup.forEach { it() }

    private fun dev(name: String, keys: Keys) = Dev(name, keys).also { d -> cleanup.add { d.stop() } }

    private fun eventually(ms: Long = 10_000, what: String = "condition", cond: () -> Boolean) {
        val end = System.currentTimeMillis() + ms
        while (System.currentTimeMillis() < end) {
            if (cond()) return
            Thread.sleep(20)
        }
        fail("$what never became true")
    }

    private val group = Keys(Protocol.masterFromCode("AAAA-BBBB-CCCC-DDDD"))

    @Test
    fun threeDevicesChainAndImages() {
        val a = dev("a", group)
        val b = dev("b", group)
        val c = dev("c", group)
        a.peers.setManual(listOf(b.addr))
        b.peers.setManual(listOf(c.addr))
        eventually(what = "links") { a.node.devices().isNotEmpty() && c.node.devices().isNotEmpty() }
        a.node.localCopy(Content(Protocol.CLIP_TEXT, text = "from a"))
        eventually(what = "b and c got it") { b.lastText() == "from a" && c.lastText() == "from a" }
        val img = ByteArray(3 shl 20) { (it % 251).toByte() }
        c.node.localCopy(Content(Protocol.CLIP_IMAGE, data = img, mime = "image/png"))
        eventually(what = "image reached a") { a.got.lastOrNull()?.data?.contentEquals(img) == true }
    }

    @Test
    fun sensitiveNotSentAndOtherGroupIgnored() {
        val a = dev("a", group)
        val b = dev("b", group)
        val x = dev("x", Keys(Protocol.masterFromCode("ZZZZ-ZZZZ-ZZZZ-ZZZZ")))
        a.peers.setManual(listOf(b.addr, x.addr))
        eventually(what = "a-b link") { a.node.devices().size == 1 }
        a.node.localCopy(Content(Protocol.CLIP_TEXT, text = "hunter2", sensitive = true))
        a.node.localCopy(Content(Protocol.CLIP_TEXT, text = "fine"))
        eventually(what = "b got fine") { b.lastText() == "fine" }
        assertTrue(b.got.none { it.text == "hunter2" })
        assertTrue(x.got.isEmpty())
        assertEquals(0, x.node.devices().size)
    }

    // ---- interop with the Go implementation (tools/interop) ----

    private fun interopEnv(): Pair<String, String>? {
        val peer = System.getenv("VOIDBRIDGE_INTEROP_PEER") ?: return null
        val code = System.getenv("VOIDBRIDGE_INTEROP_CODE") ?: return null
        return peer to code
    }

    @Test
    fun interopDirectLinkWithGo() {
        val (peerAddr, code) = interopEnv() ?: return
        val d = dev("kt-direct", Keys(Protocol.masterFromCode(code)))
        d.peers.setManual(listOf(peerAddr))
        eventually(what = "linked to Go peer") { d.node.devices().any { it.id == "go-peer" } }
        d.node.localCopy(Content(Protocol.CLIP_TEXT, text = "ping:direct ✓"))
        eventually(what = "pong from Go") { d.lastText() == "pong:direct ✓" }
        val img = ByteArray(1 shl 20) { (it * 7).toByte() }
        d.node.localCopy(Content(Protocol.CLIP_IMAGE, data = img, mime = "image/png"))
        val want = "got-image:" + java.security.MessageDigest.getInstance("SHA-256").digest(img).toHex()
        eventually(what = "Go got the image") { d.lastText() == want }
    }

    @Test
    fun interopServerWithGo() {
        val server = System.getenv("VOIDBRIDGE_INTEROP_SERVER") ?: return
        val base = ServerApi.normalizeUrl(server)
        val keys = Keys(Protocol.masterFromAccount("alice", "interop-password"))
        val me = Identity("kt-account", "Kotlin phone")
        val api = ServerApi(base)
        assertTrue(api.info().hasUsers)
        assertFailsWith<ServerException> { ServerApi(base).login("alice", Keys(Protocol.masterFromAccount("alice", "wrong")), me) }
        val login = api.login("alice", keys, me)
        assertNotNull(login.token)

        val got = CopyOnWriteArrayList<String>()
        val node = Node(me, keys, object : Node.Listener {
            override fun apply(content: Content, from: Message) { got.add(content.text) }
        })
        val relay = RelayClient(base, login.token, node, object : RelayClient.Listener {
            override fun onState(connected: Boolean, error: String?) { if (connected) connectedFlag = true }
            override fun onUnauthorized() {}
        })
        relay.start()
        cleanup.add { relay.stop(); node.shutdown() }
        eventually(what = "server connection and Go device visible") { connectedFlag && node.devices().any { it.id == "go-account" } }
        node.localCopy(Content(Protocol.CLIP_TEXT, text = "ping:server"))
        eventually(what = "pong through server") { got.lastOrNull() == "pong:server" }
    }

    @Volatile private var connectedFlag = false

    @Test
    fun normalizeUrl() {
        assertEquals("http://pi:47831", ServerApi.normalizeUrl("pi"))
        assertEquals("http://pi:9000", ServerApi.normalizeUrl("pi:9000/"))
        assertEquals("https://vb.example.com", ServerApi.normalizeUrl("https://vb.example.com"))
    }
}
