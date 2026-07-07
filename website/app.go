package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image/png"
	"net"
	"net/http"
	"os"
	"strings"
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
	defaultAnalyticsPath     = "analytics.jsonl"
)

var (
	errNotFound     = errors.New("session not found")
	errUnauthorized = errors.New("unauthorized")
	errPCGone       = errors.New("no device connected")
	errBusy         = errors.New("device is busy")
	errConflict     = errors.New("device is already connected")
	errActiveDevice = errors.New("cannot delete active device")
)

type app struct {
	hub       *hub
	analytics *analytics
}

type event struct {
	Type         string            `json:"type"`
	SID          string            `json:"sid,omitempty"`
	Name         string            `json:"name,omitempty"`
	DeviceID     string            `json:"deviceId,omitempty"`
	Device       string            `json:"device,omitempty"`
	Devices      []deviceView      `json:"devices,omitempty"`
	JoinRequests []joinRequestView `json:"joinRequests,omitempty"`
	Text         string            `json:"text,omitempty"`
	MIME         string            `json:"mime,omitempty"`
	Data         string            `json:"data,omitempty"`
}

func newApp() *app {
	h := newHub()
	return &app{
		hub:       h,
		analytics: newAnalytics(func() time.Time { return h.now() }),
	}
}

func (a *app) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w, r)

	switch {
	case r.Method == http.MethodGet && (r.URL.Path == "/" || isSessionPage(r.URL.Path)):
		a.handleIndex(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/analytics":
		a.handleAnalytics(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/privacy":
		a.handlePrivacy(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/terms":
		a.handleTerms(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/favicon.svg":
		a.handleFavicon(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/qrcode.js":
		a.handleQRCodeJS(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/app.css":
		a.handleAppCSS(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/app.js":
		a.handleAppJS(w, r)
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
	}, nil)
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
	}, nil)
}

func (a *app) serveWebSocket(w http.ResponseWriter, r *http.Request, connect func() (chan event, error), disconnect func(chan event), connected func()) {
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
	if connected != nil {
		connected()
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
	case "name":
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		a.handleSessionName(w, r, sid)
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

func (a *app) handleSessionName(w http.ResponseWriter, r *http.Request, sid string) {
	token, ok := a.deviceToken(r, sid)
	if !ok || a.hub.verifyDevice(sid, token) != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
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
	if err := a.hub.renameSession(sid, token, cleanSessionName(req.Name)); err != nil {
		writeHubError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
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
		a.recordAnalytics("device_joined")
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
	a.recordAnalytics("clipboard_shared")
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

func cleanSessionName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "Session"
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
		return "Session"
	}
	return out
}
