package com.theformofvoid.voidbridge

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.ContentValues
import android.content.Context
import android.content.Intent
import android.net.Uri
import android.os.Build
import android.os.Environment
import android.os.Handler
import android.os.Looper
import android.os.ParcelFileDescriptor
import android.provider.DocumentsContract
import android.provider.MediaStore
import android.webkit.MimeTypeMap
import android.widget.Toast
import com.theformofvoid.voidbridge.core.FileMeta
import com.theformofvoid.voidbridge.core.FileReceiver
import com.theformofvoid.voidbridge.core.FileSender
import com.theformofvoid.voidbridge.core.FileWriter
import java.io.File
import java.io.IOException
import java.io.OutputStream
import java.util.concurrent.ConcurrentHashMap

/** A file picked to send, already opened (share grants can end when the share screen closes). */
class Outgoing(val name: String, val size: Long, val mime: String, val pfd: ParcelFileDescriptor)

object Files {
    /** Turns a name chosen by another device into a plain file name. */
    fun safeName(name: String): String {
        val base = name.replace('\\', '/').substringAfterLast('/')
        val cleaned = base.map { if (it < ' ' || it in "<>:\"|?*") '_' else it }.joinToString("")
            .trim().trimEnd('.', ' ').trimStart('.')
        return cleaned.ifEmpty { "file" }.take(200)
    }

    fun mimeFor(name: String, given: String = ""): String {
        if (given.isNotEmpty() && given != "application/octet-stream") return given
        val ext = name.substringAfterLast('.', "").lowercase()
        return MimeTypeMap.getSingleton().getMimeTypeFromExtension(ext) ?: "application/octet-stream"
    }

    /** Folder used on Android 9 and older when no folder was chosen. */
    fun appDir(ctx: Context): File =
        File(ctx.getExternalFilesDir(Environment.DIRECTORY_DOWNLOADS) ?: ctx.filesDir, "VoidBridge").apply { mkdirs() }

    /** A readable description of where received files go. */
    fun folderLabel(ctx: Context, prefs: Prefs): String {
        val tree = prefs.receiveTree
        if (tree.isNotEmpty()) {
            val id = try { DocumentsContract.getTreeDocumentId(Uri.parse(tree)) } catch (_: Exception) { tree }
            return id.substringAfter(':').ifEmpty { id }.let { if (id.startsWith("primary:")) "Internal storage/$it" else it }
        }
        return if (Build.VERSION.SDK_INT >= 29) "Download/VoidBridge" else appDir(ctx).path
    }

    fun viewIntent(uri: Uri, mime: String): Intent = Intent(Intent.ACTION_VIEW)
        .setDataAndType(uri, mime)
        .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK)
}

/** Writes to an output stream, then finishes or cleans up. */
private class StreamWriter(
    private val out: OutputStream,
    private val finish: () -> String,
    private val cleanup: () -> Unit,
) : FileWriter {
    @Volatile private var done = false
    override fun write(data: ByteArray) {
        if (done) throw IOException("transfer was cancelled")
        out.write(data)
    }

    override fun commit(): String {
        done = true
        try {
            out.close()
            return finish()
        } catch (e: Exception) {
            cleanup()
            throw e
        }
    }

    override fun abort() {
        if (done) return
        done = true
        try { out.close() } catch (_: Exception) {}
        try { cleanup() } catch (_: Exception) {}
    }
}

/** Saves incoming files to Download/VoidBridge (or the chosen folder) and notifies. */
class ReceivedFiles(private val ctx: Context, private val prefs: Prefs, private val notes: TransferNotes) : FileReceiver {
    private val lastUpdate = ConcurrentHashMap<String, Long>()
    private val mimes = ConcurrentHashMap<String, String>()

    override fun begin(id: String, meta: FileMeta, from: FileSender): FileWriter {
        val name = Files.safeName(meta.name)
        val mime = Files.mimeFor(name, meta.mime)
        mimes[id] = mime
        val r = ctx.contentResolver
        val tree = prefs.receiveTree
        val w = when {
            tree.isNotEmpty() -> {
                val t = Uri.parse(tree)
                val parent = DocumentsContract.buildDocumentUriUsingTree(t, DocumentsContract.getTreeDocumentId(t))
                val uri = try {
                    DocumentsContract.createDocument(r, parent, mime, name)
                } catch (e: Exception) {
                    null
                } ?: throw IOException("can't write to the chosen folder on the phone")
                StreamWriter(r.openOutputStream(uri) ?: throw IOException("can't open the new file"), { uri.toString() }) {
                    DocumentsContract.deleteDocument(r, uri)
                }
            }
            Build.VERSION.SDK_INT >= 29 -> {
                val values = ContentValues().apply {
                    put(MediaStore.MediaColumns.DISPLAY_NAME, name)
                    put(MediaStore.MediaColumns.MIME_TYPE, mime)
                    put(MediaStore.MediaColumns.RELATIVE_PATH, Environment.DIRECTORY_DOWNLOADS + "/VoidBridge")
                    put(MediaStore.MediaColumns.IS_PENDING, 1)
                }
                val uri = r.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, values) ?: throw IOException("can't create the file in Downloads")
                StreamWriter(r.openOutputStream(uri) ?: throw IOException("can't open the new file"), {
                    r.update(uri, ContentValues().apply { put(MediaStore.MediaColumns.IS_PENDING, 0) }, null, null)
                    uri.toString()
                }) { r.delete(uri, null, null) }
            }
            else -> {
                val dir = Files.appDir(ctx)
                val tmp = File.createTempFile(".voidbridge-", ".part", dir)
                StreamWriter(tmp.outputStream(), {
                    val stem = name.substringBeforeLast('.')
                    val ext = if ('.' in name) "." + name.substringAfterLast('.') else ""
                    var f = File(dir, name)
                    var i = 2
                    while (f.exists()) f = File(dir, "$stem (${i++})$ext")
                    if (!tmp.renameTo(f)) throw IOException("can't save the file")
                    ClipProvider.receivedUri(ctx, f.name).toString()
                }) { tmp.delete() }
            }
        }
        notes.progress(id, ctx.getString(R.string.receiving, name, from.name), 0, meta.size)
        return w
    }

    override fun progress(id: String, meta: FileMeta, from: FileSender, done: Long) {
        val now = System.currentTimeMillis()
        if (now - (lastUpdate[id] ?: 0) < 500 && done < meta.size) return
        lastUpdate[id] = now
        notes.progress(id, ctx.getString(R.string.receiving, Files.safeName(meta.name), from.name), done, meta.size)
    }

    override fun received(id: String, meta: FileMeta, from: FileSender, where: String?, error: String?) {
        lastUpdate.remove(id)
        val mime = mimes.remove(id) ?: Files.mimeFor(meta.name, meta.mime)
        val name = Files.safeName(meta.name)
        if (where == null) {
            notes.done(id, ctx.getString(R.string.receive_failed, name, from.name), error, null)
        } else {
            notes.done(id, ctx.getString(R.string.received_from, name, from.name), ctx.getString(R.string.tap_to_open), Files.viewIntent(Uri.parse(where), mime))
            // Like Android's own "copied" popup when a clip arrives.
            Handler(Looper.getMainLooper()).post {
                Toast.makeText(ctx, ctx.getString(R.string.received_toast, name, from.name), Toast.LENGTH_SHORT).show()
            }
        }
    }
}

/** Notifications for files being sent and received. */
class TransferNotes(private val ctx: Context) {
    private val nm = ctx.getSystemService(NotificationManager::class.java)

    init {
        nm.createNotificationChannel(NotificationChannel(PROGRESS, ctx.getString(R.string.channel_transfers), NotificationManager.IMPORTANCE_LOW))
        nm.createNotificationChannel(NotificationChannel(DONE, ctx.getString(R.string.channel_files), NotificationManager.IMPORTANCE_DEFAULT))
    }

    private fun idFor(key: String) = 1000 + (key.hashCode() and 0x3fffffff)

    fun progress(key: String, title: String, done: Long, total: Long) {
        val n = Notification.Builder(ctx, PROGRESS)
            .setSmallIcon(R.drawable.ic_stat)
            .setContentTitle(title)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .apply {
                if (total > 0) {
                    setProgress(1000, (done * 1000 / total).toInt(), false)
                    setContentText("${size(done)} of ${size(total)}")
                } else {
                    setProgress(0, 0, true)
                }
            }
            .build()
        try { nm.notify(idFor(key), n) } catch (_: SecurityException) {}
    }

    fun done(key: String, title: String, text: String?, open: Intent?) {
        val n = Notification.Builder(ctx, DONE)
            .setSmallIcon(R.drawable.ic_stat)
            .setContentTitle(title)
            .setAutoCancel(true)
            .apply {
                if (text != null) setContentText(text).setStyle(Notification.BigTextStyle().bigText(text))
                if (open != null) setContentIntent(PendingIntent.getActivity(ctx, idFor(key), open, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT))
            }
            .build()
        try { nm.notify(idFor(key), n) } catch (_: SecurityException) {}
    }

    companion object {
        private const val PROGRESS = "transfers"
        private const val DONE = "files"

        fun size(n: Long): String = when {
            n < 1024 -> "$n B"
            n < 1024 * 1024 -> "%.0f KB".format(n / 1024.0)
            n < 1024L * 1024 * 1024 -> "%.1f MB".format(n / 1048576.0)
            else -> "%.2f GB".format(n / 1073741824.0)
        }
    }
}
