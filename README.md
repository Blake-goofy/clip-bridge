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

The desktop app opens the public ClipBridge site for its active session and passes it a private localhost bridge token. The Go process receives clipboard data, writes it to the Windows clipboard, and shows Windows notifications even if the site is only being used as the app's control surface.

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
- Set `PUBLIC_BASE_URL=https://clipbridge.cc` if the app is behind a proxy or custom domain.
- Keep one instance for now. Sessions, device names, and join-link aliases are intentionally in memory and disappear when the process restarts or the session expires.
- Analytics are appended to `analytics.jsonl` by default. Set `ANALYTICS_PATH=/path/to/analytics.jsonl` to choose a persistent volume path, or `ANALYTICS_PATH=off` to disable analytics.

## Tower of Hanoi study

- Players open `/towers`, copy their attempt code, and paste it into the corresponding Qualtrics response field. Each page load gets a fresh code. A record is created when they press **Start**, and progress is saved after legal and invalid moves. Wait for the saved message before leaving. If clipboard access is blocked, the Copy button selects the code for manual copying.
- Open `/towers/results` to look up a code or download all attempts. The CSV includes completed and incomplete attempts, one row each, with UTC timestamps, move counts, and full move history as JSON in `moves_json`. Pegs 1, 2, and 3 are left, middle, and right. Elapsed time for incomplete attempts reflects the last recorded move, or zero if none was recorded.
- The lookup and bulk CSV are public. The app saves task activity and random codes. Keep participant names, Qualtrics response IDs, and the survey-to-attempt mapping in Qualtrics. Keep the results page out of participant-facing survey content.
- Keep a single Railway instance with a [persistent volume](https://docs.railway.com/volumes). The task stores its journal at `$RAILWAY_VOLUME_MOUNT_PATH/towers-results.jsonl` by default. To override the path, set `TOWERS_RESULTS_PATH` to a dedicated file on the mounted volume. Locally it defaults to `towers-results.jsonl` beside an absolute `ANALYTICS_PATH`, or in the current directory. Results are retained until manually deleted. To remove them, export a copy if needed, stop the service, remove only the Towers journal, and start the service again.

In Qualtrics, paste this into the question’s [HTML View](https://www.qualtrics.com/support/survey-platform/survey-module/editing-questions/rich-content-editor/rich-content-editor-overview/). Add a text-entry field for the attempt code next to each task occurrence so repeat visits can be matched to the right attempt.

```html
<iframe src="https://clipbridge.cc/towers" title="Tower of Hanoi task"
        width="100%" height="760" allow="clipboard-write"></iframe>
```

Before collecting responses, preview and submit a test response in Lauren’s survey. Verify the iframe loads, the code appears in the Qualtrics export, and the same code finds the expected attempt in ClipBridge. For a custom survey domain, set `TOWERS_FRAME_ANCESTORS` to a space-separated list of allowed HTTPS origins (this replaces the default Qualtrics `.com` and `.eu` origins and subdomains). If the iframe is nested, include each ancestor origin. Try the flow on a narrow screen and with clipboard access blocked.

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
- The Windows app uses the same AES-GCM payload format. Its saved config contains server URL, app-owned session IDs, local session keys, and pairing cookies, but not clipboard contents.
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
