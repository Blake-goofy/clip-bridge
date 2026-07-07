# ClipBridge

Small Go clipboard relay for handing text and PNG images between browsers and the Windows desktop app in the same private session.

## Run

Website:

```sh
go run ./website
```

Open `http://localhost:8080`, then scan the QR code or copy the join link on another device. Any joined device can tap **Send clipboard** to relay its current clipboard text or PNG image to the other connected devices.

Windows desktop app:

```powershell
$env:CLIPBRIDGE_SERVER="http://localhost:8080"
go run ./desktop
```

The desktop app opens a local control page with a QR code. The Go process receives clipboard data, writes it to the Windows clipboard, and shows Windows notifications even after the public ClipBridge website is closed on the PC.

## Features

- Short join links and QR codes.
- Multi-device sessions. The first browser is `Device1`; later devices are `Device2`, `Device3`, and so on.
- Shared session and device managers on every device. Desktop shows sessions on the left and devices on the right; mobile has separate edge toggles.
- Device rename and delete actions sync to all connected devices.
- New devices must be approved by a device already connected to the session.
- The original browser can add, select, and close multiple join links.
- The original browser remembers its last session locally and can resume while the server still has it.
- Clipboard images are relayed as PNG payloads with a 5 MiB decoded size limit.
- Minimal aggregate analytics for successful device joins and clipboard shares.
- Installable Windows receiver app packaged with Velopack.

## Deploy

- Railway can run this as a single Go service.
- Set `PUBLIC_BASE_URL=https://clipbridge.app` if the app is behind a proxy or custom domain.
- Keep one instance for now. Sessions, device names, and join-link aliases are intentionally in memory and disappear when the process restarts or the session expires.
- Analytics are appended to `analytics.jsonl` by default. Set `ANALYTICS_PATH=/path/to/analytics.jsonl` to choose a persistent volume path, or `ANALYTICS_PATH=off` to disable analytics.

## Windows Release

Build the desktop app:

```powershell
.\scripts\build-windows.ps1 -Version 0.1.0
```

Package with Velopack:

```powershell
.\scripts\build-windows.ps1 -Version 0.1.0 -Pack
```

The script writes the app to `dist\desktop` and Velopack releases to `Releases`. `vpk` must be installed (`dotnet tool install -g vpk`) for packaging.

## Security Notes

- No accounts, database, or third-party frontend assets.
- Clipboard text and images are encrypted in the browser with AES-GCM before relay. The server relays ciphertext in memory and does not log or persist clipboard contents.
- The Windows app uses the same AES-GCM payload format. Its saved config contains server URL, session ID, local session key, and pairing cookie, but not clipboard contents.
- Analytics logs store only dates and event names for successful device joins and clipboard shares; they do not store clipboard contents, device names, IP addresses, user agents, browser IDs, or session IDs.
- Join links keep the encryption key in the URL fragment, which browsers do not send in normal HTTP requests. QR codes are rendered in the browser, so the key is not sent in a QR generation request.
- A copied link or QR code only starts a pending join; a connected device still has to allow the new device before it receives a session cookie.
- Pairing auth tokens are HttpOnly cookies.
- Closing a link requires the original browser's PC cookie and disconnects that session.
- `?text=` shortcut URLs are not supported because URLs can leak through logs, history, and referrers.

## Test

```sh
go test ./...
```
