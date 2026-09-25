package com.theformofvoid.voidbridge

import android.util.Log
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * Detects clipboard changes while VoidBridge is in the background.
 *
 * Since Android 10 only the focused app (or the keyboard) may read the
 * clipboard, and background apps don't even get change callbacks. But when
 * another app copies something, the system logs that it refused to tell us
 * ("Denying clipboard access to <our package>"). With READ_LOGS granted over
 * adb we can see that line, then briefly take focus with an invisible
 * activity to read the new clip. This is the same technique KDE Connect uses.
 */
class LogcatWatcher(private val packageName: String, private val onChange: () -> Unit) {
    @Volatile private var running = false
    @Volatile private var process: Process? = null
    @Volatile var suppressUntil = 0L

    fun start() {
        if (running) return
        running = true
        Thread(::run, "voidbridge-logcat").apply { isDaemon = true; start() }
    }

    fun stop() {
        running = false
        process?.destroy()
    }

    /** Restart the logcat process (e.g. so Android 13+ can ask for log access while we're visible). */
    fun restart() {
        process?.destroy()
    }

    private fun run() {
        while (running) {
            try {
                // Only lines logged from now on.
                val since = SimpleDateFormat("MM-dd HH:mm:ss.SSS", Locale.US).format(Date())
                val p = ProcessBuilder("logcat", "-T", since, "ClipboardService:E", "*:S")
                    .redirectErrorStream(true).start()
                process = p
                p.inputStream.bufferedReader().useLines { lines ->
                    for (line in lines) {
                        if (!running) break
                        if (line.contains(packageName) && line.contains("clipboard", ignoreCase = true)) {
                            if (System.currentTimeMillis() >= suppressUntil) onChange()
                        }
                    }
                }
            } catch (e: Exception) {
                Log.w(TAG, "logcat: $e")
            }
            if (running) Thread.sleep(1_000)
        }
    }

    companion object {
        private const val TAG = "VoidBridge"
    }
}
