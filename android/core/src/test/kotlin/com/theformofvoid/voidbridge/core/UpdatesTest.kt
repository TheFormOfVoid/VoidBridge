package com.theformofvoid.voidbridge.core

import com.sun.net.httpserver.HttpServer
import java.io.ByteArrayOutputStream
import java.io.IOException
import java.net.InetSocketAddress
import java.security.MessageDigest
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertContentEquals
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertFalse
import kotlin.test.assertTrue

class UpdatesTest {
    // Same cases as TestNewer in internal/update (Go).
    @Test
    fun newer() {
        val cases = listOf(
            Triple("v0.3.0", "v0.2.1", true),
            Triple("v0.3.0", "v0.3.0", false),
            Triple("v0.2.9", "v0.3.0", false),
            Triple("v1.0.0", "v0.99.99", true),
            Triple("v0.10.0", "v0.9.0", true),
            Triple("v0.3.0", "v0.3.0-beta.2", true),
            Triple("v0.3.1-beta.1", "v0.3.0", false),
            Triple("v0.3.0", "dev", false),
            Triple("v0.3.0", "main", false),
            Triple("v0.3.0", "dev-42", false),
            Triple("garbage", "v0.1.0", false),
        )
        for ((l, c, want) in cases) assertEquals(want, Updates.newer(l, c), "newer($l, $c)")
        assertFalse(Updates.supported("dev-12"))
        assertTrue(Updates.supported("v0.3.0"))
    }

    private var server: HttpServer? = null

    @AfterTest
    fun stop() {
        server?.stop(0)
        Updates.apiBase = "https://api.github.com"
    }

    private fun fakeGitHub(apk: ByteArray, sums: String?) {
        server?.stop(0)
        val s = HttpServer.create(InetSocketAddress("127.0.0.1", 0), 0)
        val base = "http://127.0.0.1:${s.address.port}"
        fun route(path: String, body: () -> ByteArray) = s.createContext(path) { ex ->
            val b = body()
            ex.sendResponseHeaders(200, b.size.toLong())
            ex.responseBody.use { it.write(b) }
        }
        route("/repos/${Updates.REPO}/releases/latest") {
            val assets = mutableListOf("""{"name":"${Updates.APK}","browser_download_url":"$base/dl/apk"}""")
            if (sums != null) assets.add("""{"name":"SHA256SUMS.txt","browser_download_url":"$base/dl/sums"}""")
            """{"tag_name":"v9.0.0","html_url":"https://example/rel","assets":[${assets.joinToString(",")}]}""".toByteArray()
        }
        route("/dl/apk") { apk }
        route("/dl/sums") { (sums ?: "").toByteArray() }
        s.start()
        server = s
        Updates.apiBase = base
    }

    @Test
    fun fetchChecksChecksums() {
        val apk = ByteArray(300_000) { (it * 7).toByte() }
        val good = MessageDigest.getInstance("SHA-256").digest(apk).toHex()

        fakeGitHub(apk, "$good  other.exe\n$good  ${Updates.APK}\n")
        val rel = Updates.latest()
        assertEquals("v9.0.0", rel.version)
        val out = ByteArrayOutputStream()
        Updates.fetch(rel, Updates.APK, out)
        assertContentEquals(apk, out.toByteArray())

        fakeGitHub(apk, "0".repeat(64) + "  ${Updates.APK}\n")
        val e = assertFailsWith<IOException> { Updates.fetch(Updates.latest(), Updates.APK, ByteArrayOutputStream()) }
        assertTrue("damaged" in e.message!!)

        fakeGitHub(apk, null)
        assertFailsWith<IOException> { Updates.fetch(Updates.latest(), Updates.APK, ByteArrayOutputStream()) }
    }
}
