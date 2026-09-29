package com.theformofvoid.voidbridge.core

import org.json.JSONObject
import java.io.IOException

/** What a sent file is; the JSON matches Go's protocol.FileMeta. */
data class FileMeta(val name: String, val size: Long, val mime: String = "") {
    fun toJson(): JSONObject = JSONObject().put("name", name).put("size", size).apply { if (mime.isNotEmpty()) put("mime", mime) }

    companion object {
        fun fromJson(o: JSONObject) = FileMeta(o.optString("name"), o.optLong("size", -1), o.optString("mime"))
    }
}

/** Who a received file came from. */
data class FileSender(val id: String, val name: String)

/** Receives the bytes of one incoming file. */
interface FileWriter {
    fun write(data: ByteArray)
    /** Finishes the file; returns a description of where it went (a path or URI). */
    fun commit(): String
    fun abort()
}

/** Decides where incoming files go. */
interface FileReceiver {
    /** Starts storing a file; throw to refuse it (the message is shown to the sender). */
    fun begin(id: String, meta: FileMeta, from: FileSender): FileWriter
    /** Called once a file was stored (where set) or failed (error set). */
    fun received(id: String, meta: FileMeta, from: FileSender, where: String?, error: String?)
    fun progress(id: String, meta: FileMeta, from: FileSender, done: Long) {}
}

class FileException(msg: String) : IOException(msg)
