package main

import (
	"context"
	"encoding/json"
	"html/template"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

var controlTemplate = template.Must(template.New("control").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>ClipBridge Desktop</title>
  <style>
    :root { color-scheme: light dark; font-family: ui-sans-serif, system-ui, Segoe UI, sans-serif; background: #f8f8f3; color: #171717; }
    * { box-sizing: border-box; }
    body { margin: 0; min-height: 100vh; display: grid; place-items: center; padding: 22px; }
    main { width: min(100%, 420px); display: grid; gap: 16px; text-align: center; }
    h1 { margin: 0; font-size: 34px; line-height: 1; letter-spacing: 0; }
    .tagline { margin: 0; color: #5c6257; font-weight: 650; }
    .qr { width: min(100%, 280px); aspect-ratio: 1; justify-self: center; border: 1px solid #d9dbd2; border-radius: 8px; padding: 12px; background: #fff; }
    .qr img { width: 100%; height: 100%; display: block; }
    .status { min-height: 76px; border: 1px solid #d9dbd2; border-radius: 8px; padding: 14px; display: grid; gap: 6px; background: #fff; text-align: left; }
    .status strong { font-size: 15px; }
    .muted { color: #5c6257; font-size: 13px; line-height: 1.35; word-break: break-word; }
    .actions { display: flex; gap: 10px; justify-content: center; flex-wrap: wrap; }
    button, a.button { min-height: 44px; border: 0; border-radius: 8px; padding: 0 16px; display: inline-flex; align-items: center; justify-content: center; background: #174ea6; color: #fff; font: inherit; font-weight: 750; cursor: pointer; text-decoration: none; }
    button.secondary, a.secondary { background: #fff; color: #174ea6; border: 1px solid #d9dbd2; }
    button.danger { background: #fff1f2; color: #be123c; border: 1px solid #fecaca; }
    @media (prefers-color-scheme: dark) {
      :root { background: #11130f; color: #f4f4ef; }
      .tagline, .muted { color: #b9beb1; }
      .status { background: #1b1d18; border-color: #34372f; }
      button.secondary, a.secondary { background: #252820; border-color: #34372f; color: #8ab4f8; }
      button.danger { background: #4c0519; border-color: #881337; color: #fecdd3; }
    }
  </style>
</head>
<body>
  <main>
    <header>
      <h1>ClipBridge</h1>
      <p class="tagline">Windows receiver</p>
    </header>
    <a class="qr" href="{{.JoinLink}}" title="Open join link"><img src="/qr.png" alt="QR code for pairing"></a>
    <section class="status">
      <strong>{{.Status}}</strong>
      <span class="muted">{{.Devices}} connected device{{if ne .Devices 1}}s{{end}}</span>
      <span class="muted">{{.LastEvent}}</span>
      <span class="muted">{{.JoinLink}}</span>
    </section>
    <form class="actions" method="post">
      <button formaction="/send" type="submit">Send clipboard</button>
      <button class="secondary" formaction="/copy-link" type="submit">Copy link</button>
      <button class="danger" formaction="/quit" type="submit">Quit</button>
    </form>
  </main>
  <script>setTimeout(() => location.reload(), 2500);</script>
</body>
</html>`))

type controlPage struct {
	JoinLink  string
	Status    string
	Devices   int
	LastEvent string
}

func startControlServer(ctx context.Context, app *desktopApp) (string, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		s := app.snapshot()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = controlTemplate.Execute(w, controlPage{
			JoinLink:  s["joinLink"].(string),
			Status:    s["status"].(string),
			Devices:   s["devices"].(int),
			LastEvent: s["lastEvent"].(string),
		})
	})
	mux.HandleFunc("/qr.png", func(w http.ResponseWriter, r *http.Request) {
		link := app.snapshot()["joinLink"].(string)
		png, err := qrcode.Encode(link, qrcode.Medium, 320)
		if err != nil {
			http.Error(w, "could not render qr", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(png)
	})
	mux.HandleFunc("/status.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(app.snapshot())
	})
	mux.HandleFunc("/send", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if err := app.sendClipboard(r.Context()); err != nil {
			log.Printf("send clipboard: %v", err)
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})
	mux.HandleFunc("/copy-link", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		link := app.snapshot()["joinLink"].(string)
		if err := writeClipboardTextFunc(link); err != nil {
			app.notice("Copy link failed")
		} else {
			app.notice("Link copied")
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
	})
	mux.HandleFunc("/quit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		go app.cancel()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><title>ClipBridge</title><p>ClipBridge is shutting down.</p>"))
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	server := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	go func() {
		if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("control server: %v", err)
			app.cancel()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	return "http://127.0.0.1:" + strconv.Itoa(port), nil
}
