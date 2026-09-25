package com.theformofvoid.voidbridge.core

import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString
import okio.ByteString.Companion.toByteString
import org.json.JSONObject
import java.io.IOException
import java.net.URI
import java.util.Base64
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

class ServerException(msg: String, val status: Int = 0) : IOException(msg)

/** Account calls to a VoidBridge server. Blocking; call off the main thread. */
class ServerApi(val base: String, var token: String = "") {
    data class Info(val name: String, val version: String, val signup: String, val hasUsers: Boolean)
    data class Login(val token: String, val username: String, val admin: Boolean)

    private fun call(method: String, path: String, body: JSONObject? = null): JSONObject {
        val req = Request.Builder().url(base + path)
            .method(method, body?.toString()?.toRequestBody(JSON))
            .apply { if (token.isNotEmpty()) header("Authorization", "Bearer $token") }
            .build()
        try {
            http.newCall(req).execute().use { resp ->
                val text = resp.body?.string().orEmpty()
                val o = try { JSONObject(text.ifEmpty { "{}" }) } catch (_: Exception) { JSONObject() }
                if (!resp.isSuccessful) throw ServerException(o.optString("error").ifEmpty { "server error ${resp.code}" }, resp.code)
                return o
            }
        } catch (e: ServerException) {
            throw e
        } catch (e: IOException) {
            throw ServerException("can't reach the server (${e.message})")
        }
    }

    fun info(): Info {
        val o = call("GET", "/api/info")
        if (o.optString("server") != "voidbridge") throw ServerException("that address isn't a VoidBridge server")
        if (o.optInt("protocol") != Protocol.VERSION) throw ServerException("the server speaks protocol ${o.optInt("protocol")}; update the app or the server")
        return Info(o.optString("name"), o.optString("version"), o.optString("signup"), o.optBoolean("has_users"))
    }

    private fun creds(username: String, keys: Keys, me: Identity, invite: String?) = JSONObject()
        .put("username", username)
        .put("auth_key", Base64.getEncoder().encodeToString(keys.auth))
        .put("device_id", me.id).put("device_name", me.name).put("device_kind", me.kind)
        .apply { if (!invite.isNullOrBlank()) put("invite", invite.trim()) }

    private fun login(o: JSONObject) = Login(o.getString("token"), o.optString("username"), o.optBoolean("admin")).also { token = it.token }

    fun register(username: String, keys: Keys, invite: String, me: Identity) = login(call("POST", "/api/register", creds(username, keys, me, invite)))
    fun login(username: String, keys: Keys, me: Identity) = login(call("POST", "/api/login", creds(username, keys, me, null)))
    fun logout() { call("POST", "/api/logout") }

    companion object {
        private val JSON = "application/json".toMediaType()
        val http: OkHttpClient = OkHttpClient.Builder()
            .connectTimeout(10, TimeUnit.SECONDS)
            .readTimeout(20, TimeUnit.SECONDS)
            .build()

        /** "pi", "pi:47831" or "https://x" → base URL. */
        fun normalizeUrl(s: String): String {
            var t = s.trim().trimEnd('/')
            if (t.isEmpty()) throw ServerException("enter the server address")
            if (!t.contains("://")) t = "http://$t"
            val u = try { URI(t) } catch (e: Exception) { throw ServerException("\"$s\" is not a valid server address") }
            if (u.host == null || (u.scheme != "http" && u.scheme != "https")) throw ServerException("\"$s\" is not a valid server address")
            val port = if (u.port == -1 && u.scheme == "http") Protocol.SERVER_PORT else u.port
            return "${u.scheme}://${u.host}${if (port != -1) ":$port" else ""}${u.path.orEmpty().trimEnd('/')}"
        }
    }
}

/**
 * Keeps a device connected to its server over a WebSocket, reconnecting with
 * backoff. The server relays end-to-end encrypted clips between the account's
 * devices.
 */
class RelayClient(
    private val base: String,
    private val token: String,
    private val node: Node,
    private val listener: Listener,
) {
    interface Listener {
        fun onState(connected: Boolean, error: String?)
        fun onUnauthorized()
    }

    private val client = ServerApi.http.newBuilder()
        .readTimeout(0, TimeUnit.MILLISECONDS)
        .pingInterval(20, TimeUnit.SECONDS) // also detects dead connections
        .build()
    private val timer = Executors.newSingleThreadScheduledExecutor { r -> Thread(r, "voidbridge-relay").apply { isDaemon = true } }
    @Volatile private var running = false
    @Volatile private var ws: WebSocket? = null
    private var backoff = 1_000L
    private val host = try { URI(base).host ?: base } catch (_: Exception) { base }

    fun start() {
        running = true
        connect()
    }

    fun stop() {
        running = false
        ws?.close(1000, null)
        timer.shutdownNow()
    }

    /** Reconnect now (e.g. the network changed). */
    fun kick() {
        if (!running) return
        ws?.cancel()
    }

    private inner class RelayLink(val socket: WebSocket) : Link {
        override val info = LinkInfo("server", host, "server", "Server")
        override fun send(m: Message) {
            if (!socket.send(m.encode().toByteString())) throw IOException("server connection closed")
        }
        override fun close() = socket.cancel()
    }

    private fun connect() {
        if (!running) return
        val url = "ws" + base.removePrefix("http") + "/api/sync"
        val req = Request.Builder().url(url).header("Authorization", "Bearer $token").build()
        client.newWebSocket(req, object : WebSocketListener() {
            var link: RelayLink? = null

            override fun onOpen(webSocket: WebSocket, response: Response) {
                ws = webSocket
                backoff = 1_000L
                link = RelayLink(webSocket).also { node.addLink(it) }
                listener.onState(true, null)
            }

            override fun onMessage(webSocket: WebSocket, bytes: ByteString) {
                val l = link ?: return
                val m = try { Message.decode(bytes.toByteArray()) } catch (_: Exception) { return }
                when (m.type) {
                    Message.DEVICES -> node.setRemoteDevices(l, m.peers)
                    Message.CLIP -> node.handle(l, m)
                }
            }

            override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
                webSocket.close(1000, null)
            }

            override fun onClosed(webSocket: WebSocket, code: Int, reason: String) = ended(null)

            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                if (response?.code == 401) {
                    link?.let { node.removeLink(it) }
                    running = false
                    listener.onState(false, "signed out by the server")
                    listener.onUnauthorized()
                    return
                }
                ended("can't reach the server")
            }

            private fun ended(err: String?) {
                link?.let { node.removeLink(it) }
                link = null
                ws = null
                listener.onState(false, err ?: "connection to server lost")
                if (running) {
                    val delay = backoff
                    backoff = minOf(backoff * 2, 30_000L)
                    try { timer.schedule({ connect() }, delay, TimeUnit.MILLISECONDS) } catch (_: Exception) {}
                }
            }
        })
    }
}
