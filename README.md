# VoidBridge

One clipboard for all your devices. Copy on one device, paste on every other
one. It works between any mix of Windows PCs, laptops, Android phones and
tablets. It handles text and images, like a screenshot you take with
Win+Shift+S. There's nothing to tap and nothing to confirm.

It can also send files to a chosen device: right-click a file on Windows, or
share it on Android.

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

### Do I need a server?

Usually not. If your devices are on the same Wi-Fi, or all run Tailscale, a
sync code works everywhere without anything else to set up or maintain. Start
with that.

A server is worth adding when one of these matters to you:

| Benefit | Why a sync code falls short |
|---|---|
| **Removing a lost or stolen device** | Anyone who has the code stays in the group. To lock them out you'd have to create a new code and re-enter it on every device. With a server you remove that device in one click, and your other devices keep working. |
| **Many devices** | Without a server, every device connects to every other one, so 5 devices need up to 10 connections and each phone keeps 4 open. With a server, each device keeps a single connection, which is easier on phone batteries. With 2 or 3 devices there's no difference. |
| **Devices that are rarely online together** | The server keeps your latest copy. If you copy something on your phone and the phone goes offline, your laptop still gets it when it comes online later. Without a server, a copy only arrives once both devices are online at the same time. |
| **Sharing with people who don't use Tailscale** | Only applies if the server is reachable from the internet (e.g. behind a Cloudflare Tunnel). They then just install the app and sign in with an invite. Over Tailscale alone, everyone still needs Tailscale. |

You can start with a sync code and switch to a server later: set up the
server, then sign in with your account on each device instead of the code.

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

## Sending files

Files are sent to one device you choose. They aren't synced automatically, and
there's no size limit. They go directly when both devices are on the same
network or Tailscale, and through your server otherwise, end-to-end encrypted
either way.

- **From Windows:** right-click a file → **Send with VoidBridge** → pick a
  device. On Windows 11 it's under **Show more options**. You can also drag
  files onto a device in the app, or use its **Send file** button.
- **From Android:** share a file from any app and pick **Send to a device**, or
  tap one of your devices in the share sheet. The app also has a **Send a
  file…** button.
- **Received files** go to `Downloads\VoidBridge` on Windows (they're also put
  on the clipboard, so you can paste them) and to `Download/VoidBridge` on
  Android, with a notification you can tap to open the file. You can change
  the folder in Settings on both.

The right-click menu lists devices once they've connected at least once.
Remove old ones in **Settings**.

## Updates

VoidBridge checks for new versions when it starts and every 6 hours. It only
installs stable releases, never betas, and checks each download against the
release's checksums first.

- **Windows:** with **Automatic updates** on (Settings), it installs new
  versions by itself while the window is closed, and restarts in the tray.
  With it off, a banner offers **Install and restart**.
- **Android:** with **Automatic updates** on, it downloads the new version and
  asks you to tap **Install** (on Android 12+ later updates may not need the
  tap). The first time, Android asks you to let VoidBridge install apps: use
  **Allow installing updates** in the app. With it off, you get a notification
  and can use **Check for updates**. Android only accepts an update signed
  with the same key as the installed app.
- **Server:** run the install command from [docs/SERVER.md](docs/SERVER.md)
  again.

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

The permissions survive reboots and app updates. On Android 13+, tap **Allow**
the first time the phone asks about device logs.

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

## Building it yourself

Want to build VoidBridge from source or contribute? See
[docs/DEVELOPMENT.md](docs/DEVELOPMENT.md).
