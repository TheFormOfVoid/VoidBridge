package com.theformofvoid.voidbridge.core

import okhttp3.Request
import org.json.JSONObject
import java.io.IOException
import java.io.OutputStream
import java.security.MessageDigest
import java.util.concurrent.TimeUnit

/**
 * Finds new stable releases on GitHub and downloads them, checked against the
 * release's SHA256SUMS.txt. Mirrors internal/update in Go.
 */
object Updates {
    const val REPO = "TheFormOfVoid/VoidBridge"
    const val SUMS = "SHA256SUMS.txt"
    const val APK = "VoidBridge-android.apk"

    /** The GitHub API; tests point it elsewhere. */
    @Volatile var apiBase = "https://api.github.com"

    private val http = ServerApi.http.newBuilder().readTimeout(60, TimeUnit.SECONDS).build()

    data class Release(val version: String, val page: String, val assets: Map<String, String>)

    private fun parse(v: String): Pair<List<Int>, String>? {
        val s = v.trim().removePrefix("v")
        val base = s.substringBefore('-')
        val pre = if ('-' in s) s.substringAfter('-') else ""
        val nums = base.split('.').map { it.toIntOrNull() ?: return null }
        return if (nums.size == 3 && nums.all { it >= 0 }) nums to pre else null
    }

    /** Release builds can update; "dev-…" builds can't. */
    fun supported(current: String) = parse(current) != null

    /** Whether latest is a newer stable version than current. */
    fun newer(latest: String, current: String): Boolean {
        val (l, lpre) = parse(latest) ?: return false
        val (c, cpre) = parse(current) ?: return false
        if (lpre.isNotEmpty()) return false
        for (i in 0 until 3) if (l[i] != c[i]) return l[i] > c[i]
        return cpre.isNotEmpty()
    }

    private fun get(url: String): okhttp3.Response {
        val resp = try {
            http.newCall(Request.Builder().url(url).header("User-Agent", "VoidBridge-updater").build()).execute()
        } catch (e: IOException) {
            throw IOException("can't reach GitHub (${e.message})")
        }
        if (!resp.isSuccessful) {
            resp.close()
            throw IOException("GitHub answered ${resp.code}")
        }
        return resp
    }

    /** The newest stable release ("latest" on GitHub never is a pre-release). Blocking. */
    fun latest(): Release {
        val o = get("$apiBase/repos/$REPO/releases/latest").use { JSONObject(it.body!!.string()) }
        if (o.optBoolean("prerelease") || o.optBoolean("draft") || o.optString("tag_name").isEmpty()) throw IOException("no stable release found")
        val assets = HashMap<String, String>()
        o.optJSONArray("assets")?.let { a -> for (i in 0 until a.length()) a.getJSONObject(i).let { assets[it.optString("name")] = it.optString("browser_download_url") } }
        return Release(o.getString("tag_name"), o.optString("html_url"), assets)
    }

    /** Downloads a release file into out and checks its SHA-256. On error, discard what was written. Blocking. */
    fun fetch(rel: Release, name: String, out: OutputStream) {
        val url = rel.assets[name] ?: throw IOException("${rel.version} has no $name")
        val sumsUrl = rel.assets[SUMS] ?: throw IOException("${rel.version} has no checksums, so it can't be installed automatically")
        val sums = get(sumsUrl).use { it.body!!.string() }
        val want = sums.lines().map { it.trim().split(Regex("\\s+")) }
            .lastOrNull { it.size == 2 && it[1].removePrefix("*") == name }?.get(0)?.lowercase()
        if (want?.length != 64) throw IOException("no checksum for $name in ${rel.version}")
        val md = MessageDigest.getInstance("SHA-256")
        get(url).use { resp ->
            val input = resp.body!!.byteStream()
            val buf = ByteArray(64 * 1024)
            while (true) {
                val n = input.read(buf)
                if (n < 0) break
                md.update(buf, 0, n)
                out.write(buf, 0, n)
            }
        }
        if (md.digest().toHex() != want) throw IOException("the download of $name is damaged (checksum mismatch)")
    }
}
