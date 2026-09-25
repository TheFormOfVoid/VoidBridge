# VoidBridge

One clipboard for all your devices. Copy on one device, paste on every other
one. It works between any mix of Windows PCs, laptops, Android phones and
tablets. It handles text and images, like a screenshot you take with
Win+Shift+S. There's nothing to tap and nothing to confirm.

It's built to stay connected. Devices reconnect within seconds and fall back
to other routes when one fails. Anything copied while a device was offline
reaches it when it reconnects.

## How devices connect

Pick one, or combine them:

| | What you need | Syncs when |
|---|---|---|
| **Wi-Fi** | Nothing. Devices find each other automatically. | Devices are on the same network |
| **Tailscale** | [Tailscale](https://tailscale.com) on each device | Anywhere, with no server |
| **Your own server** (e.g. a Raspberry Pi) | [Set up the server](docs/SERVER.md) once | Anywhere. Everyone who uses the server gets an account with a private clipboard. |

With a server, devices on the same Wi-Fi also connect directly. At home, sync
keeps working even if the server is down.

**Privacy:** everything is end-to-end encrypted. With a sync code, the key
comes from the code. With a server account, it comes from your password,
which never leaves your devices. The server only passes along data it can't
read. Password managers' copies are never synced.

## Install

Download from [Releases](https://github.com/TheFormOfVoid/VoidBridge/releases), or the artifacts of any
[build](https://github.com/TheFormOfVoid/VoidBridge/actions/workflows/build.yml):

- **Windows:** `VoidBridge-windows-amd64.exe` (use `arm64` for ARM laptops).
- **Android:** `VoidBridge-android.apk` (Android 8+).
- **Server:** see [docs/SERVER.md](docs/SERVER.md).

### Windows

1. Run `VoidBridge-windows-amd64.exe`. If Windows Firewall asks, allow it on
   **private networks**.
2. Open **Connections** and either:
   - **Sign in** to your server, or **Create account** (you need an invite code
     unless you're the server's first user); or
   - **Create a sync code**, then enter it on your other devices.

VoidBridge starts with Windows (hidden in the tray, and it connects by
itself). You can turn that off in **Settings**. Closing the window keeps it
running in the tray. Left-click the tray icon to reopen it, or right-click it
to pause or quit.

### Android

1. Install the APK, open VoidBridge, and sign in or join with the same account
   or code.
2. For automatic phone → other devices, see
   [One-click phone setup](#one-click-phone-setup-from-the-pc). Copies from
   your other devices always arrive automatically.

## One-click phone setup (from the PC)

Android 10+ stops background apps from reading the clipboard. Phone Link can
because it's built into the system. VoidBridge needs a permission that only a
computer can grant, **once**. The Windows app does it for you:

1. On the phone, turn on **Developer options → USB debugging**. To enable
   Developer options, tap **Settings → About phone → Build number** 7 times.
2. Plug the phone in. Or, without a cable, use **Wireless debugging** and pair
   from the Phone setup page.
3. In VoidBridge on the PC, open **Phone setup** and click **Set up this
   phone**. It downloads Google's adb tool the first time, then grants these
   in one go:
   - background clipboard access;
   - "display over other apps";
   - unrestricted battery.

The permissions survive reboots and app updates, as long as updates are signed
with the same key (see [Stable APK signing](#stable-apk-signing)). On Android
13+, tap **Allow** the first time the phone asks about device logs.

Without this step you can still send from the phone with one tap: use the
**Send clipboard** Quick Settings tile, the notification button, or **Share →
Send to all my devices**.

## Troubleshooting

| Symptom | Fix |
|---|---|
| Devices don't see each other on Wi-Fi | Some routers block device-to-device traffic ("AP/client isolation"). Add the other device's IP under **Connections → Device addresses**, or use Tailscale or a server. |
| Phone disconnects when the screen is off | Run **Phone setup** on the PC, or allow "Unrestricted battery" in the app. On Samsung, Xiaomi and Huawei, also exclude VoidBridge from the vendor's app-sleeping; see [dontkillmyapp.com](https://dontkillmyapp.com). |
| Phone → PC stopped working after a restart (Android 13+) | Open VoidBridge once so Android can ask about log access again. |
| "Signed out by the server" | The device was removed or the account disabled. Sign in again. |
| Logs | Windows: **Settings → Open log**. Phone: `adb logcat -s VoidBridge`. Server: `journalctl -u voidbridge-server`. |

Text up to 1 MB and images up to 25 MB are synced. Files aren't synced yet.

## Development

```
cmd/voidbridge/          Windows app (Go + Wails; UI in frontend/, tray, adb phone setup)
cmd/voidbridge-server/   Self-hosted server
internal/protocol        Wire format and crypto (docs/PROTOCOL.md)
internal/node            Sync engine: each copy passed to every device, newest wins
internal/peer            Direct links: Wi-Fi discovery, Tailscale, peer exchange
internal/relay, server   Server client and server
android/core             Kotlin port of protocol/node/peer/relay (plain JVM, unit-tested)
android/app              Android app
tools/interop            Go devices + server that the Kotlin tests run against
```

- `go test ./...` runs the Go tests: multi-device sync, images, offline
  catch-up, the server, and the Wi-Fi fallback.
- Windows build:
  `GOOS=windows go build -tags desktop,production -ldflags "-H windowsgui" ./cmd/voidbridge`.
- Android: `cd android && ./gradlew :core:test :app:assembleDebug`. CI also runs
  the Kotlin tests against the real Go implementation (`tools/interop`).
- Push a tag like `v0.2.0` to publish a release with every binary.

### Stable APK signing

Without a stable key, every CI build gets a different signing key. Android
then refuses to update, and uninstalling also wipes the phone-setup
permissions. Create a key once:

```
keytool -genkeypair -v -keystore voidbridge.jks -alias voidbridge -keyalg RSA -keysize 4096 -validity 36500
```

Then add these repository secrets: `VOIDBRIDGE_KEYSTORE_BASE64` (the output of
`base64 -w0 voidbridge.jks`), `VOIDBRIDGE_KEYSTORE_PASSWORD`,
`VOIDBRIDGE_KEY_ALIAS` (`voidbridge`) and `VOIDBRIDGE_KEY_PASSWORD`. Keep
`voidbridge.jks` somewhere safe.
