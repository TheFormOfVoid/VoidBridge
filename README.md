# VoidBridge

A dependable, shared clipboard for your Android phone and your Windows PC.
Copy on one device and paste on the other. There's nothing to tap and nothing
to confirm. It works like Phone Link's clipboard sync, but it's built to stay
connected.

- **Local only.** The phone talks straight to the PC over your Wi-Fi. There's
  no cloud and no account.
- **Encrypted.** Everything is encrypted with AES-256-GCM, using a key from a
  one-time pairing code.
- **Built for flaky connections:**
  - The phone reconnects within seconds, and right away when Wi-Fi comes back.
  - Heartbeats catch dead connections instead of hanging on them.
  - A copy made while disconnected is delivered on reconnect, and only the
    newest copy wins.
  - The PC is found automatically, even if its IP address changes.

## Install

Download the latest `VoidBridge-windows-amd64.exe` and `VoidBridge-android.apk`
from [Releases](../../releases). For a build of any commit, use the artifacts of
the [build workflow](../../actions/workflows/build.yml).

### PC (Windows 10/11)

1. Run `VoidBridge-windows-amd64.exe`. A ring icon appears in the tray. It's
   grey while waiting and purple once a phone is connected.
2. If Windows Firewall asks, allow VoidBridge on **Private networks**.
3. A window shows your **pairing code**. You can reopen it any time from the
   tray icon → *Pair a phone…*.

VoidBridge sets itself to start with Windows. You can turn that off in the tray
menu.

### Phone (Android 8+)

1. Install the APK. Android will ask you to allow installs from your browser or
   file manager.
2. Open VoidBridge, type the pairing code, and tap **Pair**.
3. Work through the **Setup checklist** in the app:
   - **Notifications.** Android needs these to keep the sync service running.
   - **Unrestricted battery.** Without it, Android cuts the connection when
     the screen turns off, which is a common reason other sync apps drop.
   - **Display over other apps.** VoidBridge uses this to read your clipboard
     in the background.
   - **One adb command (Android 10+),** covered below.

At this point, **PC → phone is fully automatic.**

### Automatic phone → PC (Android 10 and newer)

Android 10 stopped background apps from reading the clipboard. Phone Link
avoids this because it's built into the system. A regular app needs one extra
permission, and you can only grant it from a computer, once:

1. On the phone, turn on **Developer options → USB debugging**.
2. Install [Android platform-tools](https://developer.android.com/tools/releases/platform-tools)
   on the PC, and connect the phone by USB.
3. Run:

   ```
   adb shell pm grant com.theformofvoid.voidbridge android.permission.READ_LOGS
   ```

   The app has a *Copy command* button for this. Because your clipboard is
   already synced, you can paste the command straight into a terminal on the
   PC.
4. On Android 13 and later, the first time you open VoidBridge afterwards,
   Android asks whether the app may access device logs. Tap **Allow**.

The grant survives reboots and app updates. You can turn USB debugging off
again afterwards.

How it works: when any app copies something, Android logs that it refused to
tell VoidBridge. VoidBridge watches for that log line, then shows an invisible
window for a split second. That window has focus, so Android lets it read the
clipboard. KDE Connect uses the same technique.

**Without the adb step,** phone → PC still takes one tap. You can use the
**Send clipboard** Quick Settings tile, the button in the VoidBridge
notification, or **Share → Send to PC clipboard** from any app.

## Troubleshooting

| Symptom | Fix |
|---|---|
| Phone stuck on "Looking for your PC…" | Both devices must be on the same network. Some routers block broadcasts ("AP/client isolation"). Type the PC's IP, shown in *Pair a phone…*, into the optional address field and tap **Pair**. |
| "Wrong pairing code" | Use the code from the PC's *Pair a phone…* window. Resetting the code on the PC unpairs every phone. |
| Connection drops when the screen is off | Allow **Unrestricted battery** in the checklist. On Samsung, Xiaomi and Huawei phones, also turn off the vendor's own app-sleeping for VoidBridge. See [dontkillmyapp.com](https://dontkillmyapp.com). |
| Phone → PC stopped working after a restart (Android 13+) | Open VoidBridge once so Android can ask about log access again. |
| Logs | PC: tray → *Open log* (`%APPDATA%\VoidBridge\voidbridge.log`). Phone: `adb logcat -s VoidBridge`. |

## Limitations

VoidBridge syncs text only, up to 1 MB per copy. Images and files aren't
supported yet.

## Development

```
pc/        Go: Windows tray app (engine, protocol, discovery, Win32 clipboard)
android/   Kotlin: core/ (protocol and sync, plain JVM) + app/ (Android UI and service)
docs/      PROTOCOL.md: the wire protocol
```

- PC: `cd pc && go test ./...`. To build the exe:
  `GOOS=windows go build -ldflags "-H windowsgui" ./cmd/voidbridge`.
  `go run ./cmd/voidbridge -headless` runs it on any OS with an in-memory
  clipboard, which is handy for testing.
- Android: open `android/` in Android Studio, or run
  `./gradlew :core:test :app:assembleDebug`.
- CI runs the Kotlin client against the real Go server as an end-to-end test.
- Push a tag like `v0.1.0` to publish a release.

### Stable APK signing (optional)

Without a signing key, every CI build is signed with a different throwaway key,
so you must uninstall before installing a newer APK. To avoid that, create a
key once:

```
keytool -genkeypair -v -keystore voidbridge.jks -alias voidbridge -keyalg RSA -keysize 4096 -validity 36500
```

Then add these repository secrets: `VOIDBRIDGE_KEYSTORE_BASE64` (the output of
`base64 -w0 voidbridge.jks`), `VOIDBRIDGE_KEYSTORE_PASSWORD`,
`VOIDBRIDGE_KEY_ALIAS` and `VOIDBRIDGE_KEY_PASSWORD`.
