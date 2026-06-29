package main

import (
	_ "embed"
	"html/template"
	"log"
	"net/http"
)

//go:embed static/index.html
var indexHTML string

//go:embed static/app.css
var appCSS string

//go:embed static/app.js
var appJS string

//go:embed static/favicon.svg
var faviconSVG string

//go:embed static/qrcode.js
var qrcodeJS string

type documentPage struct {
	Title      string
	Nonce      string
	Path       string
	Paragraphs []string
}

var documentPageTemplate = template.Must(template.New("document").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.Title}} - ClipBridge</title>
  <style nonce="{{.Nonce}}">
    :root {
      color-scheme: dark;
      font-family: ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      background: #181a17;
      color: #f3f4ef;
    }
    * { box-sizing: border-box; }
    body {
      min-height: 100vh;
      margin: 0;
      padding: 22px;
      display: flex;
      flex-direction: column;
      background: #181a17;
      color: #f3f4ef;
    }
    a { color: #8ab4f8; }
    .document-page {
      width: min(100%, 760px);
      margin: 0 auto;
      flex: 1;
      display: grid;
      gap: 18px;
      align-content: start;
    }
    .page-brand {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      gap: 8px;
      color: #fff;
      font-size: 26px;
      font-weight: 850;
      line-height: 1;
      letter-spacing: 0;
      text-decoration: none;
    }
    .brand-icon {
      width: 26px;
      height: 26px;
      flex: 0 0 auto;
    }
    .brand-wrap {
      display: flex;
      justify-content: center;
      text-align: center;
      padding-top: 4px;
    }
    h1 {
      margin: 14px 0 0;
      text-align: center;
      font-size: 34px;
      line-height: 1;
      letter-spacing: 0;
    }
    .document-copy {
      border: 1px solid #3a3f36;
      border-radius: 8px;
      padding: 18px;
      background: #20231f;
      color: #d8dcce;
      font-size: 16px;
      line-height: 1.55;
    }
    .document-copy p {
      margin: 0 0 14px;
    }
    .document-copy p:last-child {
      margin-bottom: 0;
    }
    .site-footer {
      width: min(100%, 760px);
      margin: 28px auto 0;
      color: #b7bcae;
      font-size: 13px;
      font-weight: 650;
      text-align: center;
    }
    .site-footer a {
      color: #b7bcae;
      text-decoration: none;
      text-decoration-thickness: 1px;
      text-underline-offset: 3px;
    }
    .site-footer a:hover {
      color: #8ab4f8;
      text-decoration: underline;
    }
    @media (max-width: 640px) {
      body { padding: 16px; }
      h1 { font-size: 30px; }
      .document-copy { padding: 16px; }
    }
  </style>
</head>
<body>
  <main class="document-page">
    <div class="brand-wrap">
      <a class="page-brand" href="/"><img class="brand-icon" src="/favicon.svg" alt="">ClipBridge</a>
    </div>
    <h1>{{.Title}}</h1>
    <section class="document-copy">
      {{range .Paragraphs}}<p>{{.}}</p>{{end}}
    </section>
  </main>
  <footer class="site-footer">
    <a href="/">Home</a> | {{if ne .Path "/analytics"}}<a href="/analytics">Analytics</a> | {{end}}{{if ne .Path "/privacy"}}<a href="/privacy">Privacy</a> | {{end}}{{if ne .Path "/terms"}}<a href="/terms">Terms</a> | {{end}}<a href="https://github.com/Blake-goofy/clip-bridge" target="_blank" rel="noreferrer">Source code</a>
  </footer>
</body>
</html>
`))

var analyticsPageTemplate = template.Must(template.New("analytics").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>ClipBridge Analytics</title>
  <style nonce="{{.Nonce}}">
    :root {
      color-scheme: dark;
      font-family: ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      background: #181a17;
      color: #f3f4ef;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      min-height: 100vh;
      padding: 22px;
      display: flex;
      flex-direction: column;
      background: #181a17;
      color: #f3f4ef;
    }
    a { color: #8ab4f8; }
    .analytics-dashboard {
      width: min(100%, 1040px);
      margin: 0 auto;
      flex: 1;
      display: grid;
      gap: 18px;
    }
    .dashboard-header {
      display: grid;
      justify-items: center;
      gap: 14px;
      padding-bottom: 4px;
      text-align: center;
    }
    .dashboard-toolbar {
      width: 100%;
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 16px;
      text-align: left;
    }
    .home-link {
      display: inline-flex;
      align-items: center;
      justify-content: center;
      gap: 6px;
      color: #fff;
      font-size: 26px;
      font-weight: 850;
      line-height: 1;
      text-decoration: none;
    }
    .brand-icon {
      width: 26px;
      height: 26px;
      flex: 0 0 auto;
    }
    h1 {
      margin: 0;
      font-size: 34px;
      line-height: 1;
      letter-spacing: 0;
    }
    .range-picker {
      display: inline-flex;
      gap: 4px;
      padding: 3px;
      border: 1px solid #3a3f36;
      border-radius: 8px;
      background: #20231f;
    }
    .range-option {
      min-width: 42px;
      min-height: 28px;
      display: inline-grid;
      place-items: center;
      border-radius: 6px;
      color: #d8dcce;
      font-size: 12px;
      font-weight: 750;
      text-decoration: none;
    }
    .range-option.active {
      background: #174ea6;
      color: #fff;
    }
    .metric-grid {
      display: grid;
      grid-template-columns: repeat(2, minmax(0, 1fr));
      gap: 12px;
    }
    .metric-card,
    .chart-card,
    .table-card {
      border: 1px solid #d9dbd2;
      border-radius: 8px;
      background: #20231f;
      border-color: #3a3f36;
    }
    .metric-card {
      padding: 16px;
      display: grid;
      gap: 8px;
    }
    .metric-label {
      color: #b7bcae;
      font-size: 13px;
      font-weight: 750;
    }
    .metric-value {
      font-size: 34px;
      line-height: 1;
      font-weight: 850;
    }
    .chart-card {
      padding: 16px;
      display: grid;
      gap: 12px;
    }
    .section-head {
      display: flex;
      justify-content: space-between;
      gap: 12px;
      align-items: baseline;
      flex-wrap: wrap;
    }
    h2 {
      margin: 0;
      font-size: 18px;
      line-height: 1.2;
      letter-spacing: 0;
    }
    .muted {
      color: #b7bcae;
      font-size: 13px;
      font-weight: 650;
    }
    .sr-only {
      position: absolute;
      width: 1px;
      height: 1px;
      padding: 0;
      margin: -1px;
      overflow: hidden;
      clip: rect(0, 0, 0, 0);
      white-space: nowrap;
      border: 0;
    }
    .legend {
      display: flex;
      gap: 14px;
      flex-wrap: wrap;
      color: #d8dcce;
      font-size: 13px;
      font-weight: 750;
    }
    .legend-item {
      display: inline-flex;
      align-items: center;
      gap: 7px;
    }
    .legend-dot {
      width: 10px;
      height: 10px;
      border-radius: 999px;
      background: #174ea6;
    }
    .legend-dot.joins { background: #2f7d52; }
    .chart-wrap {
      position: relative;
      min-height: 260px;
    }
    .chart {
      width: 100%;
      height: auto;
      display: block;
      overflow: visible;
    }
    .grid-line,
    .axis-line {
      stroke: #3a3f36;
      stroke-width: 1;
    }
    .axis-label {
      fill: #b7bcae;
      font-size: 12px;
      font-weight: 650;
    }
    .shares-line,
    .joins-line {
      fill: none;
      stroke-width: 3;
      stroke-linecap: round;
      stroke-linejoin: round;
    }
    .shares-line { stroke: #174ea6; }
    .joins-line { stroke: #2f7d52; }
    .empty-chart {
      position: absolute;
      inset: 0;
      display: grid;
      place-items: center;
      color: #b7bcae;
      font-size: 14px;
      font-weight: 700;
      text-align: center;
      pointer-events: none;
    }
    .table-card {
      overflow: hidden;
    }
    table {
      width: 100%;
      border-collapse: collapse;
      font-size: 14px;
    }
    th,
    td {
      padding: 12px 14px;
      border-bottom: 1px solid #3a3f36;
      text-align: right;
      white-space: nowrap;
    }
    th:first-child,
    td:first-child {
      text-align: left;
    }
    th {
      color: #b7bcae;
      font-size: 12px;
      font-weight: 800;
      text-transform: uppercase;
    }
    tr:last-child td { border-bottom: 0; }
    .empty-row {
      color: #b7bcae;
      text-align: center !important;
    }
    .site-footer {
      width: min(100%, 1040px);
      margin: 28px auto 0;
      color: #b7bcae;
      font-size: 13px;
      font-weight: 650;
      text-align: center;
    }
    .site-footer a {
      color: #b7bcae;
      text-decoration: none;
      text-decoration-thickness: 1px;
      text-underline-offset: 3px;
    }
    .site-footer a:hover {
      color: #8ab4f8;
      text-decoration: underline;
    }
    @media (max-width: 640px) {
      body { padding: 16px; }
      h1 { font-size: 30px; }
      .dashboard-toolbar {
        display: grid;
        gap: 12px;
      }
      .metric-grid { grid-template-columns: 1fr; }
      .range-picker { width: 100%; }
      .range-option { flex: 1; }
      th,
      td { padding: 11px 10px; }
    }
  </style>
</head>
<body>
  <main class="analytics-dashboard">
    <header class="dashboard-header">
      <a class="home-link" href="/"><img class="brand-icon" src="/favicon.svg" alt="">ClipBridge</a>
      <div class="dashboard-toolbar">
        <h1>Analytics</h1>
        <nav class="range-picker" aria-label="Analytics range">
          {{range .RangeOptions}}
            <a class="range-option {{if .Active}}active{{end}}" href="/analytics?range={{.Value}}" {{if .Active}}aria-current="page"{{end}}>{{.Label}}</a>
          {{end}}
        </nav>
      </div>
    </header>
    {{if .AnalyticsDisabled}}
      <p>Analytics are disabled.</p>
    {{else}}
      <section class="metric-grid" aria-label="Totals for {{.RangeLabel}}">
        <div class="metric-card">
          <div class="metric-label">Clipboard shares</div>
          <div class="metric-value">{{.ClipboardShares}}</div>
        </div>
        <div class="metric-card">
          <div class="metric-label">Devices joined</div>
          <div class="metric-value">{{.DevicesJoined}}</div>
        </div>
      </section>

      <section class="chart-card" aria-labelledby="trendTitle">
        <div class="section-head">
          <div>
            <h2 id="trendTitle">{{.RangeLabel}}</h2>
            <div class="muted">Generated {{.GeneratedAt}}</div>
          </div>
          <div class="legend" aria-label="Chart legend">
            <span class="legend-item"><span class="legend-dot"></span>Shares</span>
            <span class="legend-item"><span class="legend-dot joins"></span>Joins</span>
          </div>
        </div>
        <div class="chart-wrap">
          <svg class="chart" viewBox="0 0 680 230" role="img" aria-label="Analytics trend">
            <line class="grid-line" x1="42" y1="18" x2="662" y2="18"></line>
            <line class="axis-line" x1="42" y1="198" x2="662" y2="198"></line>
            <text class="axis-label" x="8" y="22">{{.Chart.YMax}}</text>
            <text class="axis-label" x="8" y="202">0</text>
            {{range .Chart.Labels}}
              <text class="axis-label" x="{{.X}}" y="222" text-anchor="middle">{{.Text}}</text>
            {{end}}
            <polyline class="shares-line" points="{{.Chart.SharesLine}}"></polyline>
            <polyline class="joins-line" points="{{.Chart.JoinsLine}}"></polyline>
          </svg>
          {{if not .Chart.HasData}}<div class="empty-chart">No analytics for this range yet.</div>{{end}}
        </div>
      </section>

      <section class="table-card" aria-labelledby="dailyTitle">
        <table>
          <caption class="sr-only" id="dailyTitle">Daily analytics</caption>
          <thead>
            <tr>
              <th>Date</th>
              <th>Shares</th>
              <th>Joins</th>
            </tr>
          </thead>
          <tbody>
            {{range .Daily}}
              <tr>
                <td>{{.Date}}</td>
                <td>{{.ClipboardShares}}</td>
                <td>{{.DevicesJoined}}</td>
              </tr>
            {{else}}
              <tr><td class="empty-row" colspan="3">No analytics yet.</td></tr>
            {{end}}
          </tbody>
        </table>
      </section>
    {{end}}
  </main>
  <footer class="site-footer">
    <a href="/">Home</a> | <a href="/privacy">Privacy</a> | <a href="/terms">Terms</a> | <a href="https://github.com/Blake-goofy/clip-bridge" target="_blank" rel="noreferrer">Source code</a>
  </footer>
</body>
</html>
`))

func (a *app) handleIndex(w http.ResponseWriter, r *http.Request) {
	csp := "default-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'; img-src 'self' data:; connect-src 'self' ws: wss:; style-src 'self'; script-src 'self'"
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(indexHTML))
}

func (a *app) handleAnalytics(w http.ResponseWriter, r *http.Request) {
	summary, err := a.analyticsSummary(r.URL.Query().Get("range"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not read analytics")
		return
	}
	nonce, err := randomToken(16)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	summary.Nonce = nonce
	csp := "default-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'; img-src 'self'; style-src 'nonce-" + nonce + "'"
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := analyticsPageTemplate.Execute(w, summary); err != nil {
		log.Printf("ClipBridge Analytics template: %v", err)
	}
}

func (a *app) handlePrivacy(w http.ResponseWriter, r *http.Request) {
	a.writeTemplatePage(w, "Privacy Policy", documentPageTemplate, documentPage{
		Title: "Privacy Policy",
		Path:  "/privacy",
		Paragraphs: []string{
			"ClipBridge is built to move clipboard content between devices without accounts.",
			"Clipboard text and images are encrypted in your browser before relay. The server does not intentionally log, inspect, or persist clipboard contents.",
			"ClipBridge uses first-party cookies for pairing devices and keeping sessions alive.",
			"Analytics logs store dates and event names for successful device joins and clipboard shares. They do not include clipboard contents, device names, IP addresses, user agents, browser IDs, session IDs, or login identifiers.",
			"The hosting provider may keep standard infrastructure logs needed to operate the service.",
			"Clearing your browser cookies removes session cookies from that browser.",
		},
	})
}

func (a *app) handleTerms(w http.ResponseWriter, r *http.Request) {
	a.writeTemplatePage(w, "Terms of Service", documentPageTemplate, documentPage{
		Title: "Terms of Service",
		Path:  "/terms",
		Paragraphs: []string{
			"ClipBridge is provided as a lightweight clipboard handoff tool.",
			"Do not use ClipBridge for unlawful activity, abuse, or sharing content you do not have the right to share.",
			"Clipboard data may be sensitive. Only use ClipBridge with devices and people you trust, and treat join links like temporary secrets.",
			"The service is provided as-is and may change, break, or be unavailable.",
			"ClipBridge may collect limited anonymous analytics as described in the Privacy Policy.",
		},
	})
}

func (a *app) handleFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	_, _ = w.Write([]byte(faviconSVG))
}

func (a *app) handleQRCodeJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	_, _ = w.Write([]byte(qrcodeJS))
}

func (a *app) handleAppCSS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = w.Write([]byte(appCSS))
}

func (a *app) handleAppJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	_, _ = w.Write([]byte(appJS))
}

func (a *app) writeTemplatePage(w http.ResponseWriter, title string, tmpl *template.Template, data any) {
	nonce, err := randomToken(16)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	if page, ok := data.(documentPage); ok {
		page.Nonce = nonce
		data = page
	}
	csp := "default-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'; img-src 'self'; style-src 'nonce-" + nonce + "'"
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := tmpl.Execute(w, data); err != nil {
		log.Printf("%s template: %v", title, err)
	}
}
