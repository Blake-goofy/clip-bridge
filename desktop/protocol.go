package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type desktopApp struct {
	mu          sync.Mutex
	cfg         config
	configPath  string
	client      *http.Client
	session     savedSession
	joinLink    string
	status      string
	connected   bool
	devices     int
	deviceList  []deviceView
	joinList    []joinRequestView
	lastEvent   string
	cancel      context.CancelFunc
	uiURL       string
	uiToken     string
	uiOpening   bool
	logPath     string
	pendingSeen map[string]bool
	relayCancel context.CancelFunc
	relayID     int
}

type apiSession struct {
	SID     string `json:"sid"`
	LinkURL string `json:"linkURL"`
}

type relayEvent struct {
	Type         string            `json:"type"`
	SID          string            `json:"sid,omitempty"`
	Devices      []deviceView      `json:"devices,omitempty"`
	JoinRequests []joinRequestView `json:"joinRequests,omitempty"`
	Text         string            `json:"text,omitempty"`
	MIME         string            `json:"mime,omitempty"`
	Data         string            `json:"data,omitempty"`
}

type deviceView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Connected bool   `json:"connected"`
	Active    bool   `json:"active"`
}

type joinRequestView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type apiError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *apiError) Error() string {
	if e.Body == "" {
		return "server returned " + e.Status
	}
	return "server returned " + e.Status + ": " + e.Body
}

func newDesktopApp(ctx context.Context, cancel context.CancelFunc) (*desktopApp, error) {
	cfg, path, err := loadConfig()
	if err != nil {
		cfg = config{BaseURL: defaultServerBaseURL()}
		path = ""
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	restorePCCookies(jar, cfg.BaseURL, cfg)
	app := &desktopApp{
		cfg:         cfg,
		configPath:  path,
		client:      &http.Client{Jar: jar, Timeout: 20 * time.Second},
		status:      "Starting...",
		cancel:      cancel,
		logPath:     logPath(),
		pendingSeen: make(map[string]bool),
	}
	return app, nil
}

func (a *desktopApp) ensureSession(ctx context.Context) error {
	if a.cfg.Session.SID != "" && a.cfg.Session.Key != "" {
		if s, err := a.resumeSession(ctx, a.cfg.Session); err == nil {
			a.activateSession(s, "Ready.")
			return nil
		}
	}
	for _, saved := range a.cfg.Sessions {
		if saved.SID == "" || saved.Key == "" {
			continue
		}
		if s, err := a.resumeSession(ctx, saved); err == nil {
			a.activateSession(s, "Ready.")
			return nil
		}
	}
	s, err := a.createSession(ctx)
	if err != nil {
		return err
	}
	a.activateSession(s, "Ready.")
	return nil
}

func (a *desktopApp) createSession(ctx context.Context) (savedSession, error) {
	var created apiSession
	if err := a.postJSON(ctx, "/api/session", nil, &created); err != nil {
		return savedSession{}, err
	}
	key, err := randomSessionKey()
	if err != nil {
		return savedSession{}, err
	}
	cookie, secret := pcCookieFromJar(a.client.Jar, a.cfg.BaseURL, created.SID)
	if cookie == "" || secret == "" {
		return savedSession{}, errors.New("server did not return desktop session cookie")
	}
	return savedSession{SID: created.SID, Key: key, PCCookie: cookie, PCSecret: secret, DisplayName: "Windows PC"}, nil
}

func (a *desktopApp) resumeSession(ctx context.Context, s savedSession) (savedSession, error) {
	var resumed apiSession
	if err := a.postJSON(ctx, "/api/session/"+url.PathEscape(s.SID)+"/resume", map[string]string{}, &resumed); err != nil {
		return savedSession{}, err
	}
	if resumed.SID != "" {
		s.SID = resumed.SID
	}
	cookie, secret := pcCookieFromJar(a.client.Jar, a.cfg.BaseURL, s.SID)
	if cookie != "" && secret != "" {
		s.PCCookie = cookie
		s.PCSecret = secret
	}
	return s, nil
}

func (a *desktopApp) activateSession(s savedSession, status string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.session = s
	a.cfg.Session = s
	a.upsertSavedSessionLocked(s)
	a.joinLink = joinLink(a.cfg.BaseURL, s.SID, s.Key)
	a.status = status
	a.connected = false
	a.devices = 0
	a.deviceList = nil
	a.joinList = nil
	a.pendingSeen = make(map[string]bool)
	if a.configPath != "" {
		_ = saveConfig(a.configPath, a.cfg)
	}
}

func (a *desktopApp) upsertSavedSessionLocked(s savedSession) {
	for i := range a.cfg.Sessions {
		if a.cfg.Sessions[i].SID == s.SID {
			a.cfg.Sessions[i] = s
			return
		}
	}
	a.cfg.Sessions = append(a.cfg.Sessions, s)
}

func joinLink(base, sid, key string) string {
	if sid == "" || key == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/s/" + url.PathEscape(sid) + "#k=" + url.QueryEscape(key)
}

func (a *desktopApp) runRelay(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		if !a.hasSession() {
			a.setStatus("Creating session...", false)
			if err := a.ensureSession(ctx); err != nil {
				a.setError("Could not create session", err)
				sleepOrDone(ctx, 5*time.Second)
				continue
			}
		}
		if err := a.connectOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			a.setError("Reconnecting", err)
			sleepOrDone(ctx, time.Second)
		}
	}
}

func sleepOrDone(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func (a *desktopApp) hasSession() bool {
	s := a.currentSession()
	return s.SID != "" && s.Key != "" && s.PCCookie != "" && s.PCSecret != ""
}

func (a *desktopApp) connectOnce(ctx context.Context) error {
	s := a.currentSession()
	connectCtx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.relayID++
	relayID := a.relayID
	a.relayCancel = cancel
	a.mu.Unlock()
	defer func() {
		cancel()
		a.mu.Lock()
		if a.relayID == relayID {
			a.relayCancel = nil
		}
		a.mu.Unlock()
	}()
	wsURL := strings.TrimRight(a.cfg.BaseURL, "/") + "/ws/" + url.PathEscape(s.SID) + "/pc"
	c, _, err := websocket.Dial(connectCtx, wsURL, &websocket.DialOptions{HTTPClient: a.client})
	if err != nil {
		return err
	}
	defer c.Close(websocket.StatusNormalClosure, "")
	a.setStatus("Connected.", true)
	for {
		_, b, err := c.Read(connectCtx)
		if err != nil {
			return err
		}
		var ev relayEvent
		if err := json.Unmarshal(b, &ev); err != nil {
			a.notice("Received invalid event")
			continue
		}
		a.handleEvent(ctx, ev)
	}
}

func (a *desktopApp) handleEvent(ctx context.Context, ev relayEvent) {
	switch ev.Type {
	case "devices":
		connected := 0
		for _, d := range ev.Devices {
			if d.Connected {
				connected++
			}
		}
		var newPending []string
		a.mu.Lock()
		a.devices = connected
		a.deviceList = append([]deviceView(nil), ev.Devices...)
		a.joinList = append([]joinRequestView(nil), ev.JoinRequests...)
		for _, req := range ev.JoinRequests {
			if !a.pendingSeen[req.ID] {
				a.pendingSeen[req.ID] = true
				newPending = append(newPending, req.Name)
			}
		}
		a.mu.Unlock()
		for range newPending {
			a.notice("New device wants to connect")
		}
	case "clipboard.encrypted":
		payload, err := decryptPayload(a.currentSession().Key, ev.Data)
		if err != nil {
			a.notice("Could not decrypt clipboard")
			return
		}
		a.receivePayload(payload)
	case "clipboard.text":
		a.receivePayload(clipboardPayload{Type: "text", Text: ev.Text})
	case "clipboard.image":
		a.receivePayload(clipboardPayload{Type: "image", MIME: ev.MIME, Data: ev.Data})
	}
}

func (a *desktopApp) receivePayload(payload clipboardPayload) {
	switch payload.Type {
	case "image":
		if err := writeClipboardImagePNGFunc(payload.Data); err != nil {
			a.notice("Image copy failed")
			return
		}
		a.notice("Image copied to clipboard")
	case "text":
		if err := writeClipboardTextFunc(payload.Text); err != nil {
			a.notice("Text copy failed")
			return
		}
		a.notice("Text copied to clipboard")
	default:
		a.notice("Unsupported clipboard payload")
	}
}

func (a *desktopApp) sendClipboard(ctx context.Context) error {
	payload, err := readClipboardPayloadFunc()
	if err != nil {
		a.notice("Clipboard read failed")
		return err
	}
	box, err := encryptPayload(a.currentSession().Key, payload)
	if err != nil {
		return err
	}
	body := map[string]string{"mime": encryptedClipboardMIME, "data": box}
	if err := a.postJSON(ctx, "/api/session/"+url.PathEscape(a.currentSession().SID)+"/clipboard", body, nil); err != nil {
		a.notice("Send failed")
		return err
	}
	if payload.Type == "image" {
		a.notice("Image sent")
	} else {
		a.notice("Text sent")
	}
	return nil
}

func (a *desktopApp) openUI() {
	a.mu.Lock()
	if a.uiOpening {
		a.mu.Unlock()
		return
	}
	target := a.siteUIURLLocked()
	a.uiOpening = true
	a.mu.Unlock()

	if target == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := a.ensureSession(ctx)
		cancel()
		if err != nil {
			log.Printf("open ui ensure session: %v", err)
			a.notice("UI is not ready")
			a.mu.Lock()
			a.uiOpening = false
			a.mu.Unlock()
			return
		}
		a.mu.Lock()
		target = a.siteUIURLLocked()
		a.mu.Unlock()
	}
	log.Printf("open ui: %s", target)
	if err := openBrowser(target); err != nil {
		log.Printf("open ui failed: %v", err)
		a.notice("Could not open ClipBridge")
	}
	time.AfterFunc(1500*time.Millisecond, func() {
		a.mu.Lock()
		a.uiOpening = false
		a.mu.Unlock()
	})
}

func (a *desktopApp) setUIURL(value, token string) {
	a.mu.Lock()
	a.uiURL = value
	a.uiToken = token
	a.mu.Unlock()
}

func (a *desktopApp) siteUIURLLocked() string {
	if a.joinLink == "" || a.uiURL == "" || a.uiToken == "" {
		return a.joinLink
	}
	return a.joinLink + "&local=" + url.QueryEscape(a.uiURL) + "&localToken=" + url.QueryEscape(a.uiToken)
}

func (a *desktopApp) activateKnownSession(ctx context.Context, sid, key string) error {
	sid = strings.TrimSpace(sid)
	key = strings.TrimSpace(key)
	if sid == "" || key == "" {
		return errors.New("missing session")
	}
	a.mu.Lock()
	var saved savedSession
	for _, candidate := range a.cfg.Sessions {
		if candidate.SID == sid {
			saved = candidate
			break
		}
	}
	a.mu.Unlock()
	if saved.SID == "" {
		return errors.New("session is not paired with this app")
	}
	saved.Key = key
	s, err := a.resumeSession(ctx, saved)
	if err != nil {
		return err
	}
	a.activateSession(s, "Ready.")
	a.restartRelay()
	return nil
}

func (a *desktopApp) restartRelay() {
	a.mu.Lock()
	cancel := a.relayCancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *desktopApp) renameSession(ctx context.Context, sid, name string) error {
	sid = strings.TrimSpace(sid)
	name = strings.TrimSpace(name)
	if sid == "" || name == "" {
		return errors.New("missing session name")
	}
	a.restoreKnownSessionCookie(sid)
	if err := a.postJSON(ctx, "/api/session/"+url.PathEscape(sid)+"/name", map[string]string{"name": name}, nil); err != nil {
		return err
	}
	a.mu.Lock()
	for i := range a.cfg.Sessions {
		if a.cfg.Sessions[i].SID == sid {
			a.cfg.Sessions[i].DisplayName = name
		}
	}
	if a.session.SID == sid {
		a.session.DisplayName = name
		a.cfg.Session = a.session
	}
	if a.configPath != "" {
		_ = saveConfig(a.configPath, a.cfg)
	}
	a.mu.Unlock()
	return nil
}

func (a *desktopApp) renameDevice(ctx context.Context, sid, deviceID, name string) error {
	sid = strings.TrimSpace(sid)
	deviceID = strings.TrimSpace(deviceID)
	name = strings.TrimSpace(name)
	if sid == "" || deviceID == "" || name == "" {
		return errors.New("missing device name")
	}
	a.restoreKnownSessionCookie(sid)
	path := "/api/session/" + url.PathEscape(sid) + "/devices/" + url.PathEscape(deviceID) + "/name"
	return a.postJSON(ctx, path, map[string]string{"name": name}, nil)
}

func (a *desktopApp) disconnectDevice(ctx context.Context, sid, deviceID string) error {
	sid = strings.TrimSpace(sid)
	deviceID = strings.TrimSpace(deviceID)
	if sid == "" || deviceID == "" {
		return errors.New("missing device")
	}
	a.restoreKnownSessionCookie(sid)
	path := "/api/session/" + url.PathEscape(sid) + "/devices/" + url.PathEscape(deviceID) + "/disconnect"
	return a.postJSON(ctx, path, map[string]bool{}, nil)
}

func (a *desktopApp) closeSession(ctx context.Context, sid string) error {
	sid = strings.TrimSpace(sid)
	if sid == "" {
		return errors.New("missing session")
	}
	a.restoreKnownSessionCookie(sid)
	if err := a.postJSON(ctx, "/api/session/"+url.PathEscape(sid)+"/close", map[string]bool{}, nil); err != nil {
		var apiErr *apiError
		if !errors.As(err, &apiErr) || (apiErr.StatusCode != http.StatusNotFound && apiErr.StatusCode != http.StatusUnauthorized) {
			return err
		}
	}
	var next savedSession
	a.mu.Lock()
	a.cfg.Sessions = removeSavedSession(a.cfg.Sessions, sid)
	wasActive := a.session.SID == sid
	if wasActive {
		a.session = savedSession{}
		a.cfg.Session = savedSession{}
		if len(a.cfg.Sessions) > 0 {
			next = a.cfg.Sessions[0]
		}
	}
	if a.configPath != "" {
		_ = saveConfig(a.configPath, a.cfg)
	}
	a.mu.Unlock()
	if !wasActive {
		return nil
	}
	if next.SID != "" {
		s, err := a.resumeSession(ctx, next)
		if err == nil {
			a.activateSession(s, "Ready.")
			a.restartRelay()
			return nil
		}
	}
	s, err := a.createSession(ctx)
	if err != nil {
		return err
	}
	a.activateSession(s, "Ready.")
	a.restartRelay()
	return nil
}

func (a *desktopApp) restoreKnownSessionCookie(sid string) {
	a.mu.Lock()
	cfg := a.cfg
	a.mu.Unlock()
	for _, s := range cfg.Sessions {
		if s.SID == sid {
			restorePCCookie(a.client.Jar, cfg.BaseURL, s)
			return
		}
	}
	if cfg.Session.SID == sid {
		restorePCCookie(a.client.Jar, cfg.BaseURL, cfg.Session)
	}
}

func removeSavedSession(sessions []savedSession, sid string) []savedSession {
	out := sessions[:0]
	for _, s := range sessions {
		if s.SID != sid {
			out = append(out, s)
		}
	}
	return out
}

func (a *desktopApp) approveJoin(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	path := "/api/session/" + url.PathEscape(a.currentSession().SID) + "/joins/" + url.PathEscape(id) + "/approve"
	return a.postJSON(ctx, path, map[string]bool{"ok": true}, nil)
}

func (a *desktopApp) denyJoin(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}
	path := "/api/session/" + url.PathEscape(a.currentSession().SID) + "/joins/" + url.PathEscape(id) + "/deny"
	return a.postJSON(ctx, path, map[string]bool{"ok": true}, nil)
}

func (a *desktopApp) postJSON(ctx context.Context, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(a.cfg.BaseURL, "/")+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &apiError{StatusCode: resp.StatusCode, Status: resp.Status, Body: strings.TrimSpace(string(b))}
	}
	if out == nil {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (a *desktopApp) currentSession() savedSession {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.session
}

func (a *desktopApp) setStatus(status string, connected bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status = status
	a.connected = connected
}

func (a *desktopApp) setError(status string, err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status = status + "."
	a.connected = false
	a.lastEvent = err.Error()
}

func (a *desktopApp) notice(message string) {
	a.mu.Lock()
	a.lastEvent = message
	a.mu.Unlock()
	showNotificationFunc(message)
}

func (a *desktopApp) snapshot() map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return map[string]any{
		"baseURL":    a.cfg.BaseURL,
		"sid":        a.session.SID,
		"key":        a.session.Key,
		"joinLink":   a.joinLink,
		"status":     a.status,
		"connected":  a.connected,
		"devices":    a.devices,
		"deviceList": append([]deviceView(nil), a.deviceList...),
		"joinList":   append([]joinRequestView(nil), a.joinList...),
		"lastEvent":  a.lastEvent,
		"logPath":    a.logPath,
	}
}
