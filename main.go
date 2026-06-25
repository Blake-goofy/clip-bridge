package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image/png"
	"log"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/coder/websocket"
)

const (
	maxTextBytes             = 256 * 1024
	maxImageBytes            = 5 * 1024 * 1024
	maxImageBase64Bytes      = ((maxImageBytes + 2) / 3) * 4
	maxClipboardRequestBytes = maxImageBase64Bytes + 4096
	encryptedClipboardMIME   = "application/vnd.clipbridge.encrypted+json"
	unpairedTTL              = 30 * 24 * time.Hour
	idleTTL                  = 30 * 24 * time.Hour
	pendingJoinTTL           = 5 * time.Minute
)

//go:embed static/index.html
var indexHTML string

//go:embed static/favicon.svg
var faviconSVG string

//go:embed static/qrcode.js
var qrcodeJS string

var (
	errNotFound     = errors.New("session not found")
	errUnauthorized = errors.New("unauthorized")
	errPCGone       = errors.New("no device connected")
	errBusy         = errors.New("device is busy")
	errConflict     = errors.New("device is already connected")
	errActiveDevice = errors.New("cannot delete active device")
)

type app struct {
	hub *hub
}

type hub struct {
	mu       sync.Mutex
	now      func() time.Time
	sessions map[string]*session
	aliases  map[string]string
}

type session struct {
	id         string
	joinID     string
	pcHash     [32]byte
	createdAt  time.Time
	lastSeen   time.Time
	pcSend     chan event
	nextDevice int
	devices    map[string]*device
	pending    map[string]*pendingJoin
}

type device struct {
	id          string
	tokenHash   [32]byte
	name        string
	connectedAt time.Time
	lastSeen    time.Time
	send        chan event
}

type pendingJoin struct {
	id          string
	tokenHash   [32]byte
	name        string
	requestedAt time.Time
	lastSeen    time.Time
	approved    bool
}

type deviceView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Connected   bool   `json:"connected"`
	Active      bool   `json:"active"`
	ConnectedAt string `json:"connectedAt"`
}

type joinRequestView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	RequestedAt string `json:"requestedAt"`
}

type event struct {
	Type         string            `json:"type"`
	SID          string            `json:"sid,omitempty"`
	DeviceID     string            `json:"deviceId,omitempty"`
	Device       string            `json:"device,omitempty"`
	Devices      []deviceView      `json:"devices,omitempty"`
	JoinRequests []joinRequestView `json:"joinRequests,omitempty"`
	Text         string            `json:"text,omitempty"`
	MIME         string            `json:"mime,omitempty"`
	Data         string            `json:"data,omitempty"`
}

func main() {
	a := newApp()
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port
	log.Printf("clipbridge listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, a))
}

func newApp() *app {
	return &app{hub: newHub()}
}

func newHub() *hub {
	return &hub{
		now:      time.Now,
		sessions: make(map[string]*session),
		aliases:  make(map[string]string),
	}
}

func (a *app) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w, r)

	switch {
	case r.Method == http.MethodGet && (r.URL.Path == "/" || isSessionPage(r.URL.Path)):
		a.handleIndex(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/favicon.svg":
		a.handleFavicon(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/qrcode.js":
		a.handleQRCodeJS(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/session":
		a.handleCreateSession(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/ws/") && strings.HasSuffix(r.URL.Path, "/pc"):
		a.handlePCWebSocket(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/ws/") && strings.HasSuffix(r.URL.Path, "/mobile"):
		a.handleMobileWebSocket(w, r)
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/api/session/"):
		a.handleSessionPost(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (a *app) handleIndex(w http.ResponseWriter, r *http.Request) {
	nonce, err := randomToken(16)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	csp := "default-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'; img-src 'self' data:; connect-src 'self' ws: wss:; style-src 'nonce-" + nonce + "'; script-src 'nonce-" + nonce + "'"
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(strings.ReplaceAll(indexHTML, "{{NONCE}}", nonce)))
}

func (a *app) handleFavicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	_, _ = w.Write([]byte(faviconSVG))
}

func (a *app) handleQRCodeJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	_, _ = w.Write([]byte(qrcodeJS))
}

func (a *app) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	sid, pcToken, err := a.hub.createSession()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create session")
		return
	}
	setTokenCookie(w, r, pcCookieName(sid), pcToken, unpairedTTL)
	writeJSON(w, http.StatusCreated, map[string]string{
		"sid":     sid,
		"linkURL": publicBaseURL(r) + "/s/" + sid,
	})
}

func (a *app) handlePCWebSocket(w http.ResponseWriter, r *http.Request) {
	sid := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/ws/"), "/pc")
	pcToken, ok := tokenCookie(r, pcCookieName(sid))
	if !ok || a.hub.verifyPC(sid, pcToken) != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	a.serveWebSocket(w, r, func() (chan event, error) {
		return a.hub.connectPC(sid, pcToken)
	}, func(ch chan event) {
		a.hub.disconnectPC(sid, ch)
	})
}

func (a *app) handleMobileWebSocket(w http.ResponseWriter, r *http.Request) {
	sid := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/ws/"), "/mobile")
	mobileToken, ok := tokenCookie(r, mobileCookieName(sid))
	if !ok || a.hub.verifyMobile(sid, mobileToken) != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	a.serveWebSocket(w, r, func() (chan event, error) {
		return a.hub.connectMobile(sid, mobileToken)
	}, func(ch chan event) {
		a.hub.disconnectMobile(sid, ch)
	})
}

func (a *app) serveWebSocket(w http.ResponseWriter, r *http.Request, connect func() (chan event, error), disconnect func(chan event)) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	ch, err := connect()
	if err != nil {
		c.Close(websocket.StatusPolicyViolation, err.Error())
		return
	}
	defer disconnect(ch)

	ctx := c.CloseRead(r.Context())
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			b, err := json.Marshal(msg)
			if err != nil {
				return
			}
			writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err = c.Write(writeCtx, websocket.MessageText, b)
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func (a *app) handleSessionPost(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/session/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 && len(parts) != 4 {
		http.NotFound(w, r)
		return
	}
	sid, action := parts[0], parts[1]
	switch action {
	case "join":
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		a.handleJoin(w, r, sid)
	case "joins":
		if len(parts) != 4 {
			http.NotFound(w, r)
			return
		}
		a.handleJoinAction(w, r, sid, parts[2], parts[3])
	case "resume":
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		a.handleResume(w, r, sid)
	case "clipboard":
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		a.handleClipboard(w, r, sid)
	case "link":
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		a.handleRotateJoinLink(w, r, sid)
	case "close":
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		a.handleCloseSession(w, r, sid)
	case "devices":
		if len(parts) != 4 {
			http.NotFound(w, r)
			return
		}
		a.handleDeviceAction(w, r, sid, parts[2], parts[3])
	default:
		http.NotFound(w, r)
	}
}

func (a *app) handleDeviceAction(w http.ResponseWriter, r *http.Request, sid, deviceID, action string) {
	token, ok := a.deviceToken(r, sid)
	if !ok || a.hub.verifyDevice(sid, token) != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	switch action {
	case "name":
		var req struct {
			Name string `json:"name"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1024)
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
		if err := a.hub.renameDevice(sid, token, deviceID, cleanDevice(req.Name)); err != nil {
			writeHubError(w, err)
			return
		}
	case "disconnect":
		if err := a.hub.removeDevice(sid, token, deviceID); err != nil {
			writeHubError(w, err)
			return
		}
	default:
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *app) handleJoinAction(w http.ResponseWriter, r *http.Request, sid, requestID, action string) {
	token, ok := a.deviceToken(r, sid)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	switch action {
	case "approve":
		if err := a.hub.approveJoin(sid, token, requestID); err != nil {
			writeHubError(w, err)
			return
		}
	default:
		http.NotFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *app) handleRotateJoinLink(w http.ResponseWriter, r *http.Request, sid string) {
	token, ok := a.deviceToken(r, sid)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	newSID, err := a.hub.rotateJoinLink(sid, token)
	if err != nil {
		writeHubError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"sid":     newSID,
		"linkURL": publicBaseURL(r) + "/s/" + newSID,
	})
}

func (a *app) handleCloseSession(w http.ResponseWriter, r *http.Request, sid string) {
	pcToken, ok := tokenCookie(r, pcCookieName(sid))
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := a.hub.closeSession(sid, pcToken); err != nil {
		writeHubError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *app) deviceToken(r *http.Request, sid string) (string, bool) {
	if pcToken, ok := tokenCookie(r, pcCookieName(sid)); ok {
		return pcToken, true
	}
	return tokenCookie(r, mobileCookieName(sid))
}

func (a *app) handleResume(w http.ResponseWriter, r *http.Request, sid string) {
	pcToken, ok := tokenCookie(r, pcCookieName(sid))
	if !ok || a.hub.verifyPC(sid, pcToken) != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	setTokenCookie(w, r, pcCookieName(sid), pcToken, idleTTL)
	writeJSON(w, http.StatusOK, map[string]string{
		"sid":     sid,
		"linkURL": publicBaseURL(r) + "/s/" + sid,
	})
}

func (a *app) handleJoin(w http.ResponseWriter, r *http.Request, sid string) {
	var req struct {
		Device string `json:"device"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	_ = json.NewDecoder(r.Body).Decode(&req)

	existing, _ := tokenCookie(r, mobileCookieName(sid))
	deviceName := ""
	if strings.TrimSpace(req.Device) != "" {
		deviceName = cleanDevice(req.Device)
	}
	pendingToken, _ := tokenCookie(r, pendingJoinCookieName(sid))
	result, err := a.hub.requestMobileJoin(sid, existing, pendingToken, deviceName)
	if err != nil {
		writeHubError(w, err)
		return
	}
	if result.setPendingCookie {
		setTokenCookie(w, r, pendingJoinCookieName(sid), result.pendingToken, pendingJoinTTL)
	}
	if result.setMobileCookie {
		setTokenCookie(w, r, mobileCookieName(sid), result.mobileToken, idleTTL)
		clearTokenCookie(w, r, pendingJoinCookieName(sid))
	}
	if result.pending {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "pending"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "joined"})
}

func (a *app) handleClipboard(w http.ResponseWriter, r *http.Request, sid string) {
	var req struct {
		Text string `json:"text"`
		MIME string `json:"mime"`
		Data string `json:"data"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxClipboardRequestBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	msg, ok := clipboardEvent(w, req.Text, req.MIME, req.Data)
	if !ok {
		return
	}
	err := errUnauthorized
	if token, ok := a.deviceToken(r, sid); ok {
		err = a.hub.relayClipboardEvent(sid, token, msg)
	}
	if err != nil {
		switch {
		case errors.Is(err, errUnauthorized):
			writeError(w, http.StatusUnauthorized, "unauthorized")
		case errors.Is(err, errPCGone):
			writeError(w, http.StatusGone, "no device connected")
		case errors.Is(err, errBusy):
			writeError(w, http.StatusServiceUnavailable, "device is busy")
		default:
			writeError(w, http.StatusNotFound, "session not found")
		}
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]bool{"ok": true})
}

func clipboardEvent(w http.ResponseWriter, text, mime, data string) (event, bool) {
	if mime != "" || data != "" {
		if mime == encryptedClipboardMIME {
			if data == "" {
				writeError(w, http.StatusBadRequest, "missing encrypted data")
				return event{}, false
			}
			if len([]byte(data)) > maxClipboardRequestBytes {
				writeError(w, http.StatusRequestEntityTooLarge, "encrypted data is too large")
				return event{}, false
			}
			return event{Type: "clipboard.encrypted", MIME: mime, Data: data}, true
		}
		// ponytail: browsers reliably expose clipboard images as PNG; add conversion if native JPEG/WebP clipboard support becomes common.
		if mime != "image/png" {
			writeError(w, http.StatusBadRequest, "unsupported image type")
			return event{}, false
		}
		if data == "" {
			writeError(w, http.StatusBadRequest, "missing image data")
			return event{}, false
		}
		if len(data) > maxImageBase64Bytes {
			writeError(w, http.StatusRequestEntityTooLarge, "image is too large")
			return event{}, false
		}
		decoded, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid image data")
			return event{}, false
		}
		if len(decoded) > maxImageBytes {
			writeError(w, http.StatusRequestEntityTooLarge, "image is too large")
			return event{}, false
		}
		if _, err := png.DecodeConfig(bytes.NewReader(decoded)); err != nil {
			writeError(w, http.StatusBadRequest, "invalid image data")
			return event{}, false
		}
		return event{Type: "clipboard.image", MIME: mime, Data: data}, true
	}
	if len([]byte(text)) > maxTextBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "text is too large")
		return event{}, false
	}
	return event{Type: "clipboard.text", Text: text}, true
}

func writeHubError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errUnauthorized):
		writeError(w, http.StatusUnauthorized, "unauthorized")
	case errors.Is(err, errPCGone):
		writeError(w, http.StatusGone, "no device connected")
	case errors.Is(err, errBusy):
		writeError(w, http.StatusServiceUnavailable, "device is busy")
	case errors.Is(err, errActiveDevice):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, errConflict):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusNotFound, "session not found")
	}
}

type joinResult struct {
	mobileToken      string
	pendingToken     string
	setMobileCookie  bool
	setPendingCookie bool
	pending          bool
}

func (h *hub) createSession() (string, string, error) {
	pcToken, err := randomToken(32)
	if err != nil {
		return "", "", err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	for i := 0; i < 5; i++ {
		sid, err := randomSessionID()
		if err != nil {
			return "", "", err
		}
		if _, ok := h.sessions[sid]; ok {
			continue
		}
		if _, ok := h.aliases[sid]; ok {
			continue
		}
		s := &session{
			id:         sid,
			joinID:     sid,
			pcHash:     tokenHash(pcToken),
			createdAt:  now,
			lastSeen:   now,
			nextDevice: 2,
			devices:    make(map[string]*device),
			pending:    make(map[string]*pendingJoin),
		}
		deviceID, err := randomToken(9)
		if err != nil {
			return "", "", err
		}
		s.devices[deviceID] = &device{
			id:          deviceID,
			tokenHash:   tokenHash(pcToken),
			name:        "Device1",
			connectedAt: now,
			lastSeen:    now,
		}
		h.sessions[sid] = s
		h.aliases[sid] = sid
		return sid, pcToken, nil
	}
	return "", "", errors.New("could not allocate session id")
}

func (h *hub) exists(sid string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	_, ok := h.sessionLocked(sid)
	return ok
}

func (h *hub) joinLinkExists(sid string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessionLocked(sid)
	return ok && sid == s.joinID
}

func (h *hub) verifyPC(sid, token string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessionLocked(sid)
	if !ok {
		return errNotFound
	}
	if !sameToken(s.pcHash, token) {
		return errUnauthorized
	}
	if findDeviceByTokenLocked(s, token) == nil {
		return errUnauthorized
	}
	return nil
}

func (h *hub) verifyMobile(sid, token string) error {
	return h.verifyDevice(sid, token)
}

func (h *hub) verifyDevice(sid, token string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessionLocked(sid)
	if !ok {
		return errNotFound
	}
	if findDeviceByTokenLocked(s, token) == nil {
		return errUnauthorized
	}
	return nil
}

func (h *hub) connectPC(sid, token string) (chan event, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessionLocked(sid)
	if !ok {
		return nil, errNotFound
	}
	if !sameToken(s.pcHash, token) {
		return nil, errUnauthorized
	}
	if s.pcSend != nil {
		return nil, errConflict
	}
	d := findDeviceByTokenLocked(s, token)
	if d == nil {
		return nil, errUnauthorized
	}
	ch := make(chan event, 16)
	s.pcSend = ch
	d.send = ch
	d.lastSeen = now
	s.lastSeen = now
	_ = broadcastDevicesLocked(s)
	return ch, nil
}

func (h *hub) disconnectPC(sid string, ch chan event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessionLocked(sid)
	if !ok || s.pcSend != ch {
		return
	}
	s.pcSend = nil
	if d := findDeviceByChannelLocked(s, ch); d != nil {
		d.send = nil
		d.lastSeen = h.now()
	}
	s.lastSeen = h.now()
	close(ch)
	_ = broadcastDevicesLocked(s)
}

func (h *hub) requestMobileJoin(sid, existingToken, pendingToken, deviceName string) (joinResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessionLocked(sid)
	if !ok {
		return joinResult{}, errNotFound
	}
	if sid != s.joinID {
		return joinResult{}, errNotFound
	}
	if d := findDeviceByTokenLocked(s, existingToken); d != nil {
		d.lastSeen = now
		if deviceName != "" {
			d.name = deviceName
		}
		s.lastSeen = now
		_ = broadcastDevicesLocked(s)
		return joinResult{}, nil
	}
	if p := findPendingJoinByTokenLocked(s, pendingToken); p != nil {
		p.lastSeen = now
		if deviceName != "" {
			p.name = deviceName
		}
		s.lastSeen = now
		if !p.approved {
			return joinResult{pending: true}, nil
		}
		token, err := randomToken(32)
		if err != nil {
			return joinResult{}, err
		}
		if _, err := addMobileDeviceLocked(s, token, p.name, now); err != nil {
			return joinResult{}, err
		}
		delete(s.pending, p.id)
		_ = broadcastDevicesLocked(s)
		return joinResult{mobileToken: token, setMobileCookie: true}, nil
	}
	if !hasConnectedDeviceLocked(s) {
		return joinResult{}, errPCGone
	}
	pendingID, err := randomToken(9)
	if err != nil {
		return joinResult{}, err
	}
	pendingSecret, err := randomToken(32)
	if err != nil {
		return joinResult{}, err
	}
	if deviceName == "" {
		deviceName = nextDeviceNameLocked(s)
	}
	s.pending[pendingID] = &pendingJoin{
		id:          pendingID,
		tokenHash:   tokenHash(pendingSecret),
		name:        deviceName,
		requestedAt: now,
		lastSeen:    now,
	}
	s.lastSeen = now
	_ = broadcastDevicesLocked(s)
	return joinResult{pendingToken: pendingSecret, setPendingCookie: true, pending: true}, nil
}

func (h *hub) joinMobile(sid, existingToken, deviceName string) (mobileToken string, setCookie bool, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessionLocked(sid)
	if !ok {
		return "", false, errNotFound
	}
	if sid != s.joinID {
		return "", false, errNotFound
	}
	if d := findDeviceByTokenLocked(s, existingToken); d != nil {
		d.lastSeen = now
		if deviceName != "" {
			d.name = deviceName
		}
		s.lastSeen = now
		_ = broadcastDevicesLocked(s)
		return "", false, nil
	}
	token, err := randomToken(32)
	if err != nil {
		return "", false, err
	}
	if _, err := addMobileDeviceLocked(s, token, deviceName, now); err != nil {
		return "", false, err
	}
	s.lastSeen = now
	_ = broadcastDevicesLocked(s)
	return token, true, nil
}

func (h *hub) approveJoin(sid, token, requestID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessionLocked(sid)
	if !ok {
		return errNotFound
	}
	if findDeviceByTokenLocked(s, token) == nil {
		return errUnauthorized
	}
	p, ok := s.pending[requestID]
	if !ok || now.Sub(p.requestedAt) > pendingJoinTTL {
		return errNotFound
	}
	p.approved = true
	p.lastSeen = now
	s.lastSeen = now
	_ = broadcastDevicesLocked(s)
	return nil
}

func (h *hub) connectMobile(sid, token string) (chan event, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessionLocked(sid)
	if !ok {
		return nil, errNotFound
	}
	d := findDeviceByTokenLocked(s, token)
	if d == nil {
		return nil, errUnauthorized
	}
	if d.send != nil {
		return nil, errConflict
	}
	ch := make(chan event, 16)
	d.send = ch
	d.lastSeen = now
	s.lastSeen = now
	_ = broadcastDevicesLocked(s)
	return ch, nil
}

func (h *hub) disconnectMobile(sid string, ch chan event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessionLocked(sid)
	if !ok {
		return
	}
	for _, d := range s.devices {
		if d.send == ch {
			d.send = nil
			d.lastSeen = h.now()
			close(ch)
			_ = broadcastDevicesLocked(s)
			return
		}
	}
}

func (h *hub) relayClipboardToPC(sid, mobileToken, text string) error {
	return h.relayClipboard(sid, mobileToken, text)
}

func (h *hub) relayClipboardToMobile(sid, pcToken, text string) error {
	return h.relayClipboard(sid, pcToken, text)
}

func (h *hub) relayClipboard(sid, token, text string) error {
	return h.relayClipboardEvent(sid, token, event{Type: "clipboard.text", Text: text})
}

func (h *hub) relayClipboardEvent(sid, token string, msg event) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessionLocked(sid)
	if !ok {
		return errNotFound
	}
	d := findDeviceByTokenLocked(s, token)
	if d == nil {
		return errUnauthorized
	}
	d.lastSeen = now
	s.lastSeen = now
	return sendToOtherDevicesLocked(s, d, msg)
}

func (h *hub) renameDevice(sid, token, deviceID, name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessionLocked(sid)
	if !ok {
		return errNotFound
	}
	if findDeviceByTokenLocked(s, token) == nil {
		return errUnauthorized
	}
	d, ok := s.devices[deviceID]
	if !ok {
		return errNotFound
	}
	d.name = name
	d.lastSeen = now
	s.lastSeen = now
	_ = broadcastDevicesLocked(s)
	return nil
}

func (h *hub) removeDevice(sid, token, deviceID string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessionLocked(sid)
	if !ok {
		return errNotFound
	}
	requester := findDeviceByTokenLocked(s, token)
	if requester == nil {
		return errUnauthorized
	}
	d, ok := s.devices[deviceID]
	if !ok {
		return errNotFound
	}
	if d == requester {
		return errActiveDevice
	}
	delete(s.devices, deviceID)
	if d.send != nil {
		if s.pcSend == d.send {
			s.pcSend = nil
		}
		close(d.send)
		d.send = nil
	}
	s.lastSeen = now
	_ = broadcastDevicesLocked(s)
	return nil
}

func (h *hub) closeSession(sid, pcToken string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessionLocked(sid)
	if !ok {
		return errNotFound
	}
	if !sameToken(s.pcHash, pcToken) {
		return errUnauthorized
	}
	delete(h.sessions, s.id)
	for alias, target := range h.aliases {
		if target == s.id {
			delete(h.aliases, alias)
		}
	}
	closed := make(map[chan event]bool)
	closeChannel := func(ch chan event) {
		if ch == nil || closed[ch] {
			return
		}
		close(ch)
		closed[ch] = true
	}
	closeChannel(s.pcSend)
	s.pcSend = nil
	for _, d := range s.devices {
		closeChannel(d.send)
		d.send = nil
	}
	return nil
}

func (h *hub) rotateJoinLink(sid, token string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessionLocked(sid)
	if !ok {
		return "", errNotFound
	}
	requester := findDeviceByTokenLocked(s, token)
	if requester == nil {
		return "", errUnauthorized
	}
	for i := 0; i < 5; i++ {
		newSID, err := randomSessionID()
		if err != nil {
			return "", err
		}
		if _, ok := h.sessions[newSID]; ok {
			continue
		}
		if _, ok := h.aliases[newSID]; ok {
			continue
		}
		for alias, target := range h.aliases {
			if target == s.id {
				delete(h.aliases, alias)
			}
		}
		h.aliases[newSID] = s.id
		s.joinID = newSID
		// ponytail: rotating a join link is a hard device reset; use per-device grants if partial revocation is needed later.
		s.pending = make(map[string]*pendingJoin)
		for id, d := range s.devices {
			if d == requester {
				continue
			}
			delete(s.devices, id)
			if d.send != nil {
				if s.pcSend == d.send {
					s.pcSend = nil
				}
				close(d.send)
				d.send = nil
			}
		}
		s.lastSeen = now
		_ = broadcastLocked(s, event{Type: "link", SID: newSID})
		_ = broadcastDevicesLocked(s)
		return newSID, nil
	}
	return "", errors.New("could not allocate session id")
}

func (h *hub) cleanupLocked(now time.Time) {
	// ponytail: in-memory sessions are single-process only; use Redis/pubsub if Railway needs multiple instances.
	for sid, s := range h.sessions {
		for id, p := range s.pending {
			if now.Sub(p.lastSeen) > pendingJoinTTL {
				delete(s.pending, id)
			}
		}
		unpairedExpired := len(s.devices) == 0 && now.Sub(s.createdAt) > unpairedTTL
		idleExpired := now.Sub(s.lastSeen) > idleTTL
		if unpairedExpired || idleExpired {
			delete(h.sessions, sid)
			for alias, target := range h.aliases {
				if target == sid {
					delete(h.aliases, alias)
				}
			}
			closed := make(map[chan event]bool)
			closeChannel := func(ch chan event) {
				if ch == nil || closed[ch] {
					return
				}
				close(ch)
				closed[ch] = true
			}
			closeChannel(s.pcSend)
			for _, d := range s.devices {
				closeChannel(d.send)
			}
		}
	}
}

func (h *hub) sessionLocked(sid string) (*session, bool) {
	if s, ok := h.sessions[sid]; ok {
		return s, true
	}
	canonical, ok := h.aliases[sid]
	if !ok {
		return nil, false
	}
	s, ok := h.sessions[canonical]
	return s, ok
}

func findDeviceByTokenLocked(s *session, token string) *device {
	if token == "" {
		return nil
	}
	for _, d := range s.devices {
		if sameToken(d.tokenHash, token) {
			return d
		}
	}
	return nil
}

func findDeviceByChannelLocked(s *session, ch chan event) *device {
	for _, d := range s.devices {
		if d.send == ch {
			return d
		}
	}
	return nil
}

func findPendingJoinByTokenLocked(s *session, token string) *pendingJoin {
	if token == "" {
		return nil
	}
	for _, p := range s.pending {
		if sameToken(p.tokenHash, token) {
			return p
		}
	}
	return nil
}

func hasConnectedDeviceLocked(s *session) bool {
	for _, d := range s.devices {
		if d.send != nil {
			return true
		}
	}
	return false
}

func addMobileDeviceLocked(s *session, token, deviceName string, now time.Time) (*device, error) {
	deviceID, err := randomToken(9)
	if err != nil {
		return nil, err
	}
	if deviceName == "" {
		deviceName = nextDeviceNameLocked(s)
	}
	d := &device{
		id:          deviceID,
		tokenHash:   tokenHash(token),
		name:        deviceName,
		connectedAt: now,
		lastSeen:    now,
	}
	s.devices[deviceID] = d
	return d, nil
}

func nextDeviceNameLocked(s *session) string {
	name := "Device" + strconv.Itoa(s.nextDevice)
	s.nextDevice++
	return name
}

func devicesEventLocked(s *session, active *device) event {
	return event{Type: "devices", Devices: deviceViewsLocked(s, active), JoinRequests: joinRequestViewsLocked(s)}
}

func deviceViewsLocked(s *session, active *device) []deviceView {
	devices := make([]deviceView, 0, len(s.devices))
	for _, d := range s.devices {
		devices = append(devices, deviceView{
			ID:          d.id,
			Name:        d.name,
			Connected:   d.send != nil,
			Active:      d == active,
			ConnectedAt: d.connectedAt.Format(time.RFC3339),
		})
	}
	sort.Slice(devices, func(i, j int) bool {
		return devices[i].ConnectedAt < devices[j].ConnectedAt
	})
	return devices
}

func joinRequestViewsLocked(s *session) []joinRequestView {
	requests := make([]joinRequestView, 0, len(s.pending))
	for _, p := range s.pending {
		if p.approved {
			continue
		}
		requests = append(requests, joinRequestView{
			ID:          p.id,
			Name:        p.name,
			RequestedAt: p.requestedAt.Format(time.RFC3339),
		})
	}
	sort.Slice(requests, func(i, j int) bool {
		return requests[i].RequestedAt < requests[j].RequestedAt
	})
	return requests
}

func broadcastDevicesLocked(s *session) error {
	sent := 0
	busy := 0
	for _, d := range s.devices {
		if d.send == nil {
			continue
		}
		if err := sendLocked(d.send, devicesEventLocked(s, d)); err != nil {
			busy++
			continue
		}
		sent++
	}
	if sent > 0 {
		return nil
	}
	if busy > 0 {
		return errBusy
	}
	return errPCGone
}

func broadcastLocked(s *session, msg event) error {
	sent := 0
	busy := 0
	for _, d := range s.devices {
		if d.send == nil {
			continue
		}
		if err := sendLocked(d.send, msg); err != nil {
			busy++
			continue
		}
		sent++
	}
	if sent > 0 {
		return nil
	}
	if busy > 0 {
		return errBusy
	}
	return errPCGone
}

func sendToOtherDevicesLocked(s *session, sender *device, msg event) error {
	sent := 0
	busy := 0
	for _, d := range s.devices {
		if d == sender || d.send == nil {
			continue
		}
		if err := sendLocked(d.send, msg); err != nil {
			busy++
			continue
		}
		sent++
	}
	if sent > 0 {
		return nil
	}
	if busy > 0 {
		return errBusy
	}
	return errPCGone
}

func sendLocked(ch chan event, msg event) error {
	select {
	case ch <- msg:
		return nil
	default:
		return errBusy
	}
}

func sendToPCLocked(s *session, msg event) error {
	if s.pcSend == nil {
		return errPCGone
	}
	return sendLocked(s.pcSend, msg)
}

func sendToMobilesLocked(s *session, msg event) error {
	return broadcastLocked(s, msg)
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func tokenHash(token string) [32]byte {
	return sha256.Sum256([]byte(token))
}

func sameToken(hash [32]byte, token string) bool {
	got := tokenHash(token)
	return subtle.ConstantTimeCompare(hash[:], got[:]) == 1
}

func pcCookieName(sid string) string {
	return "cb_pc_" + sid
}

func mobileCookieName(sid string) string {
	return "cb_mobile_" + sid
}

func pendingJoinCookieName(sid string) string {
	return "cb_join_" + sid
}

func tokenCookie(r *http.Request, name string) (string, bool) {
	c, err := r.Cookie(name)
	if err != nil || c.Value == "" {
		return "", false
	}
	return c.Value, true
}

func setTokenCookie(w http.ResponseWriter, r *http.Request, name, value string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   int(ttl.Seconds()),
		HttpOnly: true,
		Secure:   secureCookie(r),
		SameSite: http.SameSiteStrictMode,
	})
}

func clearTokenCookie(w http.ResponseWriter, r *http.Request, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secureCookie(r),
		SameSite: http.SameSiteStrictMode,
	})
}

func setSecurityHeaders(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Permissions-Policy", "clipboard-read=(self), clipboard-write=(self)")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	if secureCookie(r) {
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Security-Policy", "default-src 'none'; base-uri 'none'; frame-ancestors 'none'")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func isSessionPage(path string) bool {
	if !strings.HasPrefix(path, "/s/") {
		return false
	}
	return strings.Count(strings.Trim(path, "/"), "/") == 1
}

func publicBaseURL(r *http.Request) string {
	if base := strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/"); base != "" {
		return base
	}
	scheme := forwardedProto(r)
	if scheme == "" {
		if secureCookie(r) {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	if scheme == "http" && !isLocalHost(r.Host) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func forwardedProto(r *http.Request) string {
	proto := r.Header.Get("X-Forwarded-Proto")
	if i := strings.IndexByte(proto, ','); i >= 0 {
		proto = proto[:i]
	}
	proto = strings.ToLower(strings.TrimSpace(proto))
	if proto == "http" || proto == "https" {
		return proto
	}
	return ""
}

func secureCookie(r *http.Request) bool {
	return r.TLS != nil || forwardedProto(r) == "https"
}

func isLocalHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	host = strings.ToLower(host)
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func cleanDevice(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "Mobile"
	}
	var b strings.Builder
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			b.WriteByte(' ')
			continue
		}
		if unicode.IsPrint(r) {
			b.WriteRune(r)
		}
		if b.Len() >= 40 {
			break
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "Mobile"
	}
	return out
}
