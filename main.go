package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/coder/websocket"
	qrcode "github.com/skip2/go-qrcode"
)

const (
	maxTextBytes = 256 * 1024
	unpairedTTL  = 10 * time.Minute
	idleTTL      = 30 * time.Minute
)

//go:embed static/index.html
var indexHTML string

var (
	errNotFound      = errors.New("session not found")
	errUnauthorized  = errors.New("unauthorized")
	errAlreadyPaired = errors.New("session already paired")
	errPCGone        = errors.New("no device connected")
	errBusy          = errors.New("device is busy")
	errConflict      = errors.New("device is already connected")
)

type app struct {
	hub *hub
}

type hub struct {
	mu       sync.Mutex
	now      func() time.Time
	sessions map[string]*session
}

type session struct {
	id         string
	pcHash     [32]byte
	mobileHash [32]byte
	mobileSet  bool
	device     string
	createdAt  time.Time
	lastSeen   time.Time
	pcSend     chan event
	mobileSend chan event
}

type event struct {
	Type      string `json:"type"`
	Device    string `json:"device,omitempty"`
	Connected bool   `json:"connected,omitempty"`
	Text      string `json:"text,omitempty"`
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
	}
}

func (a *app) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w, r)

	switch {
	case r.Method == http.MethodGet && (r.URL.Path == "/" || isSessionPage(r.URL.Path)):
		a.handleIndex(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/api/session":
		a.handleCreateSession(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/qr/") && strings.HasSuffix(r.URL.Path, ".png"):
		a.handleQR(w, r)
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
	csp := "default-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'; img-src 'self'; connect-src 'self' ws: wss:; style-src 'nonce-" + nonce + "'; script-src 'nonce-" + nonce + "'"
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(strings.ReplaceAll(indexHTML, "{{NONCE}}", nonce)))
}

func (a *app) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	sid, pcToken, err := a.hub.createSession()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "could not create session")
		return
	}
	setTokenCookie(w, r, pcCookieName(sid), pcToken, unpairedTTL)
	writeJSON(w, http.StatusCreated, map[string]string{
		"sid":   sid,
		"qrURL": publicBaseURL(r) + "/qr/" + sid + ".png",
	})
}

func (a *app) handleQR(w http.ResponseWriter, r *http.Request) {
	sid := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/qr/"), ".png")
	if !a.hub.exists(sid) {
		http.NotFound(w, r)
		return
	}
	png, err := qrcode.Encode(publicBaseURL(r)+"/s/"+sid, qrcode.Medium, 256)
	if err != nil {
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(png)
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
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	sid, action := parts[0], parts[1]
	switch action {
	case "join":
		a.handleJoin(w, r, sid)
	case "clipboard":
		a.handleClipboard(w, r, sid)
	default:
		http.NotFound(w, r)
	}
}

func (a *app) handleJoin(w http.ResponseWriter, r *http.Request, sid string) {
	var req struct {
		Device string `json:"device"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	_ = json.NewDecoder(r.Body).Decode(&req)

	existing, _ := tokenCookie(r, mobileCookieName(sid))
	mobileToken, setCookie, err := a.hub.joinMobile(sid, existing, cleanDevice(req.Device))
	if err != nil {
		status := http.StatusNotFound
		if errors.Is(err, errAlreadyPaired) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	if setCookie {
		setTokenCookie(w, r, mobileCookieName(sid), mobileToken, idleTTL)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (a *app) handleClipboard(w http.ResponseWriter, r *http.Request, sid string) {
	var req struct {
		Text string `json:"text"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxTextBytes+1024)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if len([]byte(req.Text)) > maxTextBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "text is too large")
		return
	}
	err := errUnauthorized
	if pcToken, ok := tokenCookie(r, pcCookieName(sid)); ok && a.hub.verifyPC(sid, pcToken) == nil {
		err = a.hub.relayClipboardToMobile(sid, pcToken, req.Text)
	} else if mobileToken, ok := tokenCookie(r, mobileCookieName(sid)); ok {
		err = a.hub.relayClipboardToPC(sid, mobileToken, req.Text)
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
		sid, err := randomToken(18)
		if err != nil {
			return "", "", err
		}
		if _, ok := h.sessions[sid]; ok {
			continue
		}
		h.sessions[sid] = &session{
			id:        sid,
			pcHash:    tokenHash(pcToken),
			createdAt: now,
			lastSeen:  now,
		}
		return sid, pcToken, nil
	}
	return "", "", errors.New("could not allocate session id")
}

func (h *hub) exists(sid string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	_, ok := h.sessions[sid]
	return ok
}

func (h *hub) verifyPC(sid, token string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessions[sid]
	if !ok {
		return errNotFound
	}
	if !sameToken(s.pcHash, token) {
		return errUnauthorized
	}
	return nil
}

func (h *hub) verifyMobile(sid, token string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessions[sid]
	if !ok {
		return errNotFound
	}
	if !s.mobileSet || !sameToken(s.mobileHash, token) {
		return errUnauthorized
	}
	return nil
}

func (h *hub) connectPC(sid, token string) (chan event, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessions[sid]
	if !ok {
		return nil, errNotFound
	}
	if !sameToken(s.pcHash, token) {
		return nil, errUnauthorized
	}
	if s.pcSend != nil {
		return nil, errConflict
	}
	ch := make(chan event, 16)
	s.pcSend = ch
	s.lastSeen = now
	if s.mobileSend != nil {
		_ = sendToPCLocked(s, event{Type: "presence", Device: s.device, Connected: true})
	}
	return ch, nil
}

func (h *hub) disconnectPC(sid string, ch chan event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[sid]
	if !ok || s.pcSend != ch {
		return
	}
	delete(h.sessions, sid)
	close(ch)
	if s.mobileSend != nil {
		close(s.mobileSend)
	}
}

func (h *hub) joinMobile(sid, existingToken, device string) (mobileToken string, setCookie bool, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessions[sid]
	if !ok {
		return "", false, errNotFound
	}
	if s.mobileSet {
		if existingToken != "" && sameToken(s.mobileHash, existingToken) {
			s.lastSeen = now
			return "", false, nil
		}
		return "", false, errAlreadyPaired
	}
	token, err := randomToken(32)
	if err != nil {
		return "", false, err
	}
	s.mobileHash = tokenHash(token)
	s.mobileSet = true
	s.device = device
	s.lastSeen = now
	return token, true, nil
}

func (h *hub) connectMobile(sid, token string) (chan event, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessions[sid]
	if !ok {
		return nil, errNotFound
	}
	if !s.mobileSet || !sameToken(s.mobileHash, token) {
		return nil, errUnauthorized
	}
	if s.mobileSend != nil {
		return nil, errConflict
	}
	ch := make(chan event, 16)
	s.mobileSend = ch
	s.lastSeen = now
	_ = sendToPCLocked(s, event{Type: "presence", Device: s.device, Connected: true})
	return ch, nil
}

func (h *hub) disconnectMobile(sid string, ch chan event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[sid]
	if !ok || s.mobileSend != ch {
		return
	}
	s.mobileSend = nil
	close(ch)
	_ = sendToPCLocked(s, event{Type: "presence", Device: s.device, Connected: false})
}

func (h *hub) relayClipboardToPC(sid, mobileToken, text string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessions[sid]
	if !ok {
		return errNotFound
	}
	if !s.mobileSet || !sameToken(s.mobileHash, mobileToken) {
		return errUnauthorized
	}
	s.lastSeen = now
	return sendToPCLocked(s, event{Type: "clipboard.text", Text: text})
}

func (h *hub) relayClipboardToMobile(sid, pcToken, text string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.now()
	h.cleanupLocked(now)
	s, ok := h.sessions[sid]
	if !ok {
		return errNotFound
	}
	if !sameToken(s.pcHash, pcToken) {
		return errUnauthorized
	}
	s.lastSeen = now
	return sendToMobileLocked(s, event{Type: "clipboard.text", Text: text})
}

func (h *hub) cleanupLocked(now time.Time) {
	// ponytail: in-memory sessions are single-process only; use Redis/pubsub if Railway needs multiple instances.
	for sid, s := range h.sessions {
		unpairedExpired := !s.mobileSet && now.Sub(s.createdAt) > unpairedTTL
		idleExpired := now.Sub(s.lastSeen) > idleTTL
		if unpairedExpired || idleExpired {
			delete(h.sessions, sid)
			if s.pcSend != nil {
				close(s.pcSend)
			}
			if s.mobileSend != nil {
				close(s.mobileSend)
			}
		}
	}
}

func sendToPCLocked(s *session, msg event) error {
	if s.pcSend == nil {
		return errPCGone
	}
	select {
	case s.pcSend <- msg:
		return nil
	default:
		return errBusy
	}
}

func sendToMobileLocked(s *session, msg event) error {
	if s.mobileSend == nil {
		return errPCGone
	}
	select {
	case s.mobileSend <- msg:
		return nil
	default:
		return errBusy
	}
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
