package com.theformofvoid.voidbridge

import android.content.ContentProvider
import android.content.ContentValues
import android.content.Context
import android.database.Cursor
import android.database.MatrixCursor
import android.net.Uri
import android.os.ParcelFileDescriptor
import android.provider.OpenableColumns
import java.io.File
import java.io.FileNotFoundException

/**
 * Serves images received from other devices, so they can go on the Android
 * clipboard (which holds images as content:// URIs). The clipboard grants
 * read access to whichever app pastes.
 */
class ClipProvider : ContentProvider() {
    override fun onCreate() = true

    private fun file(uri: Uri): File {
        val name = uri.lastPathSegment ?: throw FileNotFoundException()
        if (name.contains('/') || name.startsWith(".")) throw FileNotFoundException()
        val f = File(dir(context!!), name)
        if (!f.exists()) throw FileNotFoundException()
        return f
    }

    override fun openFile(uri: Uri, mode: String): ParcelFileDescriptor =
        ParcelFileDescriptor.open(file(uri), ParcelFileDescriptor.MODE_READ_ONLY)

    override fun getType(uri: Uri): String = mimeFor(uri.lastPathSegment ?: "")

    override fun query(uri: Uri, projection: Array<out String>?, selection: String?, selectionArgs: Array<out String>?, sortOrder: String?): Cursor {
        val f = file(uri)
        val cols = projection ?: arrayOf(OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE)
        return MatrixCursor(cols, 1).apply {
            addRow(cols.map { if (it == OpenableColumns.SIZE) f.length() else if (it == OpenableColumns.DISPLAY_NAME) f.name else null })
        }
    }

    override fun insert(uri: Uri, values: ContentValues?): Uri? = null
    override fun delete(uri: Uri, selection: String?, selectionArgs: Array<out String>?) = 0
    override fun update(uri: Uri, values: ContentValues?, selection: String?, selectionArgs: Array<out String>?) = 0

    companion object {
        fun authority(ctx: Context) = ctx.packageName + ".clips"

        private fun dir(ctx: Context) = File(ctx.cacheDir, "clips").apply { mkdirs() }

        private val extensions = mapOf("image/png" to "png", "image/jpeg" to "jpg", "image/gif" to "gif", "image/webp" to "webp", "image/bmp" to "bmp")

        fun mimeFor(name: String) = extensions.entries.firstOrNull { name.endsWith("." + it.value) }?.key ?: "image/png"

        /** Saves an image and returns a content URI for it. Keeps only the last few. */
        fun save(ctx: Context, id: String, data: ByteArray, mime: String): Uri {
            val d = dir(ctx)
            d.listFiles()?.sortedByDescending { it.lastModified() }?.drop(4)?.forEach { it.delete() }
            val name = "clip-" + id.filter { it.isLetterOrDigit() } + "." + (extensions[mime] ?: "png")
            File(d, name).writeBytes(data)
            return Uri.parse("content://${authority(ctx)}/$name")
        }
    }
}
