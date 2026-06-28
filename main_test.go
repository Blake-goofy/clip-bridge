package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestSessionIDUsesURLSafeToken(t *testing.T) {
	h := newHub()
	sid, _, err := h.createSession()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(sid)
	if err != nil || len(raw) != 9 {
		t.Fatalf("session id is not a 9-byte URL-safe token: sid=%q len=%d err=%v", sid, len(raw), err)
	}
}

func TestMultipleMobilesAndCookieReuse(t *testing.T) {
	h := newHub()
	sid, _, err := h.createSession()
	if err != nil {
		t.Fatal(err)
	}
	token, setCookie, err := h.joinMobile(sid, "", "iPhone")
	if err != nil {
		t.Fatal(err)
	}
	if !setCookie || token == "" {
		t.Fatal("first join did not issue a mobile token")
	}
	if _, setCookie, err := h.joinMobile(sid, token, "iPhone"); err != nil || setCookie {
		t.Fatalf("same mobile should rejoin without a new cookie: setCookie=%v err=%v", setCookie, err)
	}
	secondToken, setCookie, err := h.joinMobile(sid, "wrong", "Other")
	if err != nil {
		t.Fatal(err)
	}
	if !setCookie || secondToken == "" || secondToken == token {
		t.Fatalf("second mobile should receive a distinct cookie: setCookie=%v token=%q", setCookie, secondToken)
	}
}

func TestDefaultDeviceNamesAndMobileRenameBroadcast(t *testing.T) {
	h := newHub()
	sid, _, err := h.createSession()
	if err != nil {
		t.Fatal(err)
	}
	firstToken, _, err := h.joinMobile(sid, "", "")
	if err != nil {
		t.Fatal(err)
	}
	secondToken, _, err := h.joinMobile(sid, "", "")
	if err != nil {
		t.Fatal(err)
	}
	secondCh, err := h.connectMobile(sid, secondToken)
	if err != nil {
		t.Fatal(err)
	}

	initial := readDevicesEvent(t, "second", secondCh)
	names := map[string]string{}
	var firstID string
	for _, d := range initial.Devices {
		names[d.Name] = d.ID
		if d.Name == "Device2" {
			firstID = d.ID
		}
	}
	for _, want := range []string{"Device1", "Device2", "Device3"} {
		if names[want] == "" {
			t.Fatalf("missing default device name %q in %#v", want, initial.Devices)
		}
	}
	if err := h.renameDevice(sid, firstToken, firstID, "Blake phone"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		updated := readDevicesEvent(t, "second", secondCh)
		for _, d := range updated.Devices {
			if d.ID == firstID && d.Name == "Blake phone" {
				return
			}
		}
	}
	t.Fatal("renamed device was not broadcast")
}

func TestSessionRenameBroadcastsToConnectedAndNewDevices(t *testing.T) {
	h := newHub()
	sid, pcToken, err := h.createSession()
	if err != nil {
		t.Fatal(err)
	}
	pcCh, err := h.connectPC(sid, pcToken)
	if err != nil {
		t.Fatal(err)
	}
	mobileToken, _, err := h.joinMobile(sid, "", "Phone")
	if err != nil {
		t.Fatal(err)
	}
	mobileCh, err := h.connectMobile(sid, mobileToken)
	if err != nil {
		t.Fatal(err)
	}

	if err := h.renameSession(sid, pcToken, "Personal"); err != nil {
		t.Fatal(err)
	}
	readSessionNameEvent(t, "pc", pcCh, "Personal")
	readSessionNameEvent(t, "mobile", mobileCh, "Personal")

	tabletToken, _, err := h.joinMobile(sid, "", "Tablet")
	if err != nil {
		t.Fatal(err)
	}
	tabletCh, err := h.connectMobile(sid, tabletToken)
	if err != nil {
		t.Fatal(err)
	}
	readSessionNameEvent(t, "tablet", tabletCh, "Personal")
}

func TestRotateJoinLinkDropsOtherDevicesAndStaleLink(t *testing.T) {
	h := newHub()
	sid, pcToken, err := h.createSession()
	if err != nil {
		t.Fatal(err)
	}
	pcCh, err := h.connectPC(sid, pcToken)
	if err != nil {
		t.Fatal(err)
	}
	<-pcCh
	mobileToken, _, err := h.joinMobile(sid, "", "")
	if err != nil {
		t.Fatal(err)
	}
	readDevicesEvent(t, "pc", pcCh)
	mobileCh, err := h.connectMobile(sid, mobileToken)
	if err != nil {
		t.Fatal(err)
	}
	readDevicesEvent(t, "mobile", mobileCh)

	newSID, err := h.rotateJoinLink(sid, pcToken)
	if err != nil {
		t.Fatal(err)
	}
	if newSID == sid {
		t.Fatal("rotated join link reused original sid")
	}
	if !h.exists(newSID) {
		t.Fatal("rotated join link does not resolve")
	}
	if h.joinLinkExists(sid) {
		t.Fatal("old join link should be stale")
	}
	linkEvent := readLinkEvent(t, "pc", pcCh)
	if linkEvent.SID != newSID {
		t.Fatalf("link event sid = %q, want %q", linkEvent.SID, newSID)
	}
	devices := readDevicesEvent(t, "pc", pcCh)
	if len(devices.Devices) != 1 || devices.Devices[0].Name != "Device1" {
		t.Fatalf("devices after rotated join = %#v, want only requester", devices.Devices)
	}
	select {
	case _, ok := <-mobileCh:
		if ok {
			t.Fatal("stale mobile channel should be closed")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stale mobile channel should be closed")
	}
	if err := h.verifyMobile(sid, mobileToken); err == nil {
		t.Fatal("stale mobile token should not verify")
	}
	if err := h.relayClipboard(sid, mobileToken, "hello"); err != errUnauthorized {
		t.Fatalf("stale mobile send err = %v, want unauthorized", err)
	}
	if _, _, err := h.joinMobile(sid, "", ""); err == nil {
		t.Fatal("old join link should not accept new devices")
	}
	if _, _, err := h.joinMobile(newSID, "", ""); err != nil {
		t.Fatal(err)
	}
	devices = readDevicesEvent(t, "pc", pcCh)
	if len(devices.Devices) != 2 {
		t.Fatalf("devices after new link join = %d, want 2: %#v", len(devices.Devices), devices.Devices)
	}
}

func TestExpiryCleanup(t *testing.T) {
	h := newHub()
	now := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return now }
	sid, _, err := h.createSession()
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(unpairedTTL + time.Second)
	if h.exists(sid) {
		t.Fatal("unpaired session should expire")
	}
}

func TestNoDeliveryWhenPCGone(t *testing.T) {
	a := newApp()
	sid, _, err := a.hub.createSession()
	if err != nil {
		t.Fatal(err)
	}
	mobileToken, _, err := a.hub.joinMobile(sid, "", "iPhone")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.hub.relayClipboardToPC(sid, mobileToken, "hello"); err != errPCGone {
		t.Fatalf("expected errPCGone, got %v", err)
	}
}

func TestPCDisconnectRetainsSession(t *testing.T) {
	h := newHub()
	sid, pcToken, err := h.createSession()
	if err != nil {
		t.Fatal(err)
	}
	ch, err := h.connectPC(sid, pcToken)
	if err != nil {
		t.Fatal(err)
	}
	h.disconnectPC(sid, ch)
	if err := h.verifyPC(sid, pcToken); err != nil {
		t.Fatalf("pc session should survive disconnect: %v", err)
	}
}

func TestPCBroadcastsClipboardToConnectedMobiles(t *testing.T) {
	h := newHub()
	sid, pcToken, err := h.createSession()
	if err != nil {
		t.Fatal(err)
	}
	firstToken, _, err := h.joinMobile(sid, "", "First")
	if err != nil {
		t.Fatal(err)
	}
	secondToken, _, err := h.joinMobile(sid, "", "Second")
	if err != nil {
		t.Fatal(err)
	}
	firstCh, err := h.connectMobile(sid, firstToken)
	if err != nil {
		t.Fatal(err)
	}
	secondCh, err := h.connectMobile(sid, secondToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.relayClipboardToMobile(sid, pcToken, "hello all"); err != nil {
		t.Fatal(err)
	}
	for name, ch := range map[string]chan event{"first": firstCh, "second": secondCh} {
		got := readClipboardEvent(t, name, ch)
		if got.Text != "hello all" {
			t.Fatalf("%s got wrong text %q", name, got.Text)
		}
	}
}

func TestPCReceivesDeviceListUpdates(t *testing.T) {
	h := newHub()
	sid, pcToken, err := h.createSession()
	if err != nil {
		t.Fatal(err)
	}
	pcCh, err := h.connectPC(sid, pcToken)
	if err != nil {
		t.Fatal(err)
	}
	<-pcCh
	firstToken, _, err := h.joinMobile(sid, "", "First")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.connectMobile(sid, firstToken); err != nil {
		t.Fatal(err)
	}
	secondToken, _, err := h.joinMobile(sid, "", "Second")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.connectMobile(sid, secondToken); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 6; i++ {
		latest := readDevicesEvent(t, "pc", pcCh)
		connected := 0
		for _, d := range latest.Devices {
			if d.Connected {
				connected++
			}
		}
		if connected == 3 {
			return
		}
	}
	t.Fatal("pc did not receive all connected devices")
}

func TestDeviceListTracksLiveConnections(t *testing.T) {
	h := newHub()
	sid, pcToken, err := h.createSession()
	if err != nil {
		t.Fatal(err)
	}
	pcCh, err := h.connectPC(sid, pcToken)
	if err != nil {
		t.Fatal(err)
	}
	<-pcCh
	mobileToken, _, err := h.joinMobile(sid, "", "Phone")
	if err != nil {
		t.Fatal(err)
	}
	mobileCh, err := h.connectMobile(sid, mobileToken)
	if err != nil {
		t.Fatal(err)
	}
	readDevicesEvent(t, "mobile", mobileCh)

	h.disconnectPC(sid, pcCh)
	devices := readDevicesEvent(t, "mobile", mobileCh)
	got := map[string]bool{}
	for _, d := range devices.Devices {
		got[d.Name] = d.Connected
	}
	if got["Device1"] {
		t.Fatalf("pc should be offline after websocket disconnect: %#v", devices.Devices)
	}
	if !got["Phone"] {
		t.Fatalf("phone should stay connected: %#v", devices.Devices)
	}
}

func TestMobileCanSendAfterPCReconnect(t *testing.T) {
	h := newHub()
	sid, pcToken, err := h.createSession()
	if err != nil {
		t.Fatal(err)
	}
	pcCh, err := h.connectPC(sid, pcToken)
	if err != nil {
		t.Fatal(err)
	}
	<-pcCh
	mobileToken, _, err := h.joinMobile(sid, "", "Phone")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.connectMobile(sid, mobileToken); err != nil {
		t.Fatal(err)
	}
	h.disconnectPC(sid, pcCh)
	if err := h.relayClipboard(sid, mobileToken, "miss"); err != errPCGone {
		t.Fatalf("send after pc disconnect = %v, want pc gone", err)
	}
	pcCh, err = h.connectPC(sid, pcToken)
	if err != nil {
		t.Fatal(err)
	}
	readDevicesEvent(t, "pc", pcCh)
	if err := h.relayClipboard(sid, mobileToken, "hit"); err != nil {
		t.Fatal(err)
	}
	if got := readClipboardEvent(t, "pc", pcCh); got.Text != "hit" {
		t.Fatalf("pc got %q", got.Text)
	}
}

func TestResumeRequiresPCCookie(t *testing.T) {
	a := newApp()
	ts := httptest.NewServer(a)
	defer ts.Close()

	res, err := http.Post(ts.URL+"/api/session", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var created struct {
		SID string `json:"sid"`
	}
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	pcCookie := findCookie(t, res.Cookies(), pcCookieName(created.SID))

	unauthorized, err := http.Post(ts.URL+"/api/session/"+created.SID+"/resume", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("resume without pc cookie status %d", unauthorized.StatusCode)
	}

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/session/"+created.SID+"/resume", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(pcCookie)
	resumed, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Body.Close()
	if resumed.StatusCode != http.StatusOK {
		t.Fatalf("resume with pc cookie status %d", resumed.StatusCode)
	}
}

func TestDesktopCanRenameAndDisconnectDevice(t *testing.T) {
	a := newApp()
	sid, pcToken, err := a.hub.createSession()
	if err != nil {
		t.Fatal(err)
	}
	mobileToken, _, err := a.hub.joinMobile(sid, "", "Mobile")
	if err != nil {
		t.Fatal(err)
	}
	a.hub.mu.Lock()
	deviceID := findDeviceByTokenLocked(a.hub.sessions[sid], mobileToken).id
	a.hub.mu.Unlock()
	if deviceID == "" {
		t.Fatal("missing joined device")
	}

	if err := a.hub.renameDevice(sid, pcToken, deviceID, "Blake phone"); err != nil {
		t.Fatal(err)
	}
	a.hub.mu.Lock()
	got := a.hub.sessions[sid].devices[deviceID].name
	a.hub.mu.Unlock()
	if got != "Blake phone" {
		t.Fatalf("device name = %q", got)
	}
	if err := a.hub.removeDevice(sid, pcToken, deviceID); err != nil {
		t.Fatal(err)
	}
	if err := a.hub.verifyMobile(sid, mobileToken); err == nil {
		t.Fatal("removed device token should no longer verify")
	}
}

func TestCannotDeleteActiveDevice(t *testing.T) {
	h := newHub()
	sid, pcToken, err := h.createSession()
	if err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	pcID := findDeviceByTokenLocked(h.sessions[sid], pcToken).id
	h.mu.Unlock()
	if err := h.removeDevice(sid, pcToken, pcID); err != errActiveDevice {
		t.Fatalf("delete active device err = %v, want %v", err, errActiveDevice)
	}
	if err := h.verifyPC(sid, pcToken); err != nil {
		t.Fatalf("active device should remain valid: %v", err)
	}
}

func TestCloseSessionRequiresPCToken(t *testing.T) {
	h := newHub()
	sid, pcToken, err := h.createSession()
	if err != nil {
		t.Fatal(err)
	}
	mobileToken, _, err := h.joinMobile(sid, "", "Phone")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.closeSession(sid, mobileToken); err != errUnauthorized {
		t.Fatalf("mobile close err = %v, want unauthorized", err)
	}
	pcCh, err := h.connectPC(sid, pcToken)
	if err != nil {
		t.Fatal(err)
	}
	readDevicesEvent(t, "pc", pcCh)
	if err := h.closeSession(sid, pcToken); err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-pcCh:
		if ok {
			t.Fatal("closed session should close pc channel")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closed session should close pc channel")
	}
	if h.exists(sid) {
		t.Fatal("closed session should not exist")
	}
}

func TestEncryptedClipboardPayloadRelay(t *testing.T) {
	a := newApp()
	sid, pcToken, err := a.hub.createSession()
	if err != nil {
		t.Fatal(err)
	}
	pcCh, err := a.hub.connectPC(sid, pcToken)
	if err != nil {
		t.Fatal(err)
	}
	mobileToken, _, err := a.hub.joinMobile(sid, "", "Phone")
	if err != nil {
		t.Fatal(err)
	}

	box := `{"v":1,"iv":"abc","ct":"def"}`
	req := httptest.NewRequest(http.MethodPost, "/api/session/"+sid+"/clipboard", strings.NewReader(`{"mime":"`+encryptedClipboardMIME+`","data":`+strconv.Quote(box)+`}`))
	req.AddCookie(&http.Cookie{Name: mobileCookieName(sid), Value: mobileToken})
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("encrypted send status = %d, body %s", w.Code, w.Body.String())
	}
	got := readEncryptedEvent(t, "pc", pcCh)
	if got.MIME != encryptedClipboardMIME || got.Data != box {
		t.Fatalf("wrong encrypted event %#v", got)
	}
}

func TestOversizedPayloadRejected(t *testing.T) {
	a := newApp()
	sid, _, err := a.hub.createSession()
	if err != nil {
		t.Fatal(err)
	}
	mobileToken, _, err := a.hub.joinMobile(sid, "", "iPhone")
	if err != nil {
		t.Fatal(err)
	}
	body := `{"text":"` + strings.Repeat("a", maxTextBytes+1) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/session/"+sid+"/clipboard", strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: mobileCookieName(sid), Value: mobileToken})
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != http.StatusRequestEntityTooLarge && w.Code != http.StatusBadRequest {
		t.Fatalf("expected payload rejection, got %d", w.Code)
	}
}

func TestImageClipboardPayloadRelayAndValidation(t *testing.T) {
	a := newApp()
	sid, pcToken, err := a.hub.createSession()
	if err != nil {
		t.Fatal(err)
	}
	pcCh, err := a.hub.connectPC(sid, pcToken)
	if err != nil {
		t.Fatal(err)
	}
	mobileToken, _, err := a.hub.joinMobile(sid, "", "Phone")
	if err != nil {
		t.Fatal(err)
	}

	pngData := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII="
	req := httptest.NewRequest(http.MethodPost, "/api/session/"+sid+"/clipboard", strings.NewReader(`{"mime":"image/png","data":"`+pngData+`"}`))
	req.AddCookie(&http.Cookie{Name: mobileCookieName(sid), Value: mobileToken})
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("image send status = %d, body %s", w.Code, w.Body.String())
	}
	got := readImageEvent(t, "pc", pcCh)
	if got.MIME != "image/png" || got.Data != pngData {
		t.Fatalf("wrong image event %#v", got)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/session/"+sid+"/clipboard", strings.NewReader(`{"mime":"image/jpeg","data":"/9j/"}`))
	req.AddCookie(&http.Cookie{Name: mobileCookieName(sid), Value: mobileToken})
	w = httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("jpeg send status = %d, want 400", w.Code)
	}
}

func TestIndexUsesNeutralClipboardUI(t *testing.T) {
	for _, old := range []string{"Windows", "iPhone", "Safari", "No phone", "Connected to PC", "Send Clipboard", "<title>Clip Bridge</title>", "Waiting.", "receiver", "sender", "New Session", "Add link", "border-radius: 24px", "box-shadow", "notif-overlay", "qrWrap.classList.toggle(\"hidden\", event.connected)", "transform: translateX(100%)", "syncJoinedPaneLayout", "copyConnectURL", "connectURL", "copy-link", "?fragment=", "qrQuery", "session-select", "session-delete", "reveal-delete", "swipeStartX", "readClipboardImage"} {
		if strings.Contains(indexHTML, old) {
			t.Fatalf("index still contains platform-specific or removed UI text %q", old)
		}
	}
	for _, want := range []string{"<title>ClipBridge</title>", `<script nonce="{{NONCE}}" src="/qrcode.js"></script>`, `<link rel="icon" href="/favicon.svg" type="image/svg+xml">`, `<h1><img class="brand-icon" src="/favicon.svg" alt="">ClipBridge</h1>`, "Secure clipboard handoff", "Send clipboard", "Peek clipboard", "Blake Becker |", "Source code", "localStorage", "/resume", "sessionPane", "sessionPaneToggle", "devicePane", "devicePaneToggle", "body.pc-mode .desktop-pane", "syncPaneLayout", "mobileQr", "toggleQR", "drawQRCode", "ClipBridgeQRCode", "addSession", "Add session", "sessionList", "sessionModal", "sessionNameInput", "defaultSessionName", "connectedDeviceCount", "edit-session-button", "notice.peek", "notice.peek .notif-status", "notif-image", "readClipboardContent", "readClipboardPreview", "text/plain", "showClipboardPeek", "setupPeekButton", "peekAutoHideMs = 2500", "(hover: none) and (pointer: coarse)", "position: fixed", "padding-right: 0", "encryptedClipboardMIME", "copySelectedLink", "sessionLink", "/name", "event.type === \"session\"", "updateSessionName", "deviceCount", "/disconnect", "pcActions", "pcMessages", "mobileMessages", "navigator.clipboard.writeText(text || \"\")", "position: sticky", "onMiddleClick", "onauxclick", "deleteDevice(device)", "width: 100vw", "border-radius: 8px 8px 0 0"} {
		if !strings.Contains(indexHTML, want) {
			t.Fatalf("index is missing chat UI marker %q", want)
		}
	}
}

func TestIndexCSPAllowsRenderedImagePreviews(t *testing.T) {
	a := newApp()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if got := w.Header().Get("Content-Security-Policy"); !strings.Contains(got, "img-src 'self' data:") {
		t.Fatalf("CSP = %q, want data: image previews allowed", got)
	}
}

func TestFaviconServed(t *testing.T) {
	a := newApp()
	req := httptest.NewRequest(http.MethodGet, "/favicon.svg", nil)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("favicon status = %d", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "image/svg+xml; charset=utf-8" {
		t.Fatalf("favicon content type = %q", got)
	}
	if !strings.Contains(w.Body.String(), `stroke="currentColor"`) {
		t.Fatal("favicon does not use theme-aware currentColor stroke")
	}
}

func TestQRCodeJSServed(t *testing.T) {
	a := newApp()
	req := httptest.NewRequest(http.MethodGet, "/qrcode.js", nil)
	w := httptest.NewRecorder()
	a.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("qrcode js status = %d", w.Code)
	}
	if got := w.Header().Get("Content-Type"); got != "application/javascript; charset=utf-8" {
		t.Fatalf("qrcode js content type = %q", got)
	}
	if body := w.Body.String(); !strings.Contains(body, "ClipBridgeQRCode") || strings.Contains(body, "?fragment=") {
		t.Fatal("qrcode js should expose local QR renderer without fragment transport")
	}
}

func TestWebSocketClipboardRelayFromPCToMobile(t *testing.T) {
	a := newApp()
	ts := httptest.NewServer(a)
	defer ts.Close()

	res, err := http.Post(ts.URL+"/api/session", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var created struct {
		SID string `json:"sid"`
	}
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	pcCookie := findCookie(t, res.Cookies(), pcCookieName(created.SID))

	pcWSURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/" + created.SID + "/pc"
	pcConn, _, err := websocket.Dial(context.Background(), pcWSURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": []string{pcCookie.String()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pcConn.Close(websocket.StatusNormalClosure, "")

	mobileCookie := joinMobileWithApprovalHTTP(t, ts.URL, created.SID, pcCookie, pcConn, "Device")

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/" + created.SID + "/mobile"
	conn, _, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": []string{mobileCookie.String()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/session/"+created.SID+"/clipboard", bytes.NewBufferString(`{"text":"hello mobile"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(pcCookie)
	sendRes, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer sendRes.Body.Close()
	if sendRes.StatusCode != http.StatusAccepted {
		t.Fatalf("send status %d", sendRes.StatusCode)
	}

	msg := readClipboardWS(t, conn)
	if msg.Text != "hello mobile" {
		t.Fatalf("wrong message %#v", msg)
	}
}

func TestWebSocketClipboardRelay(t *testing.T) {
	a := newApp()
	ts := httptest.NewServer(a)
	defer ts.Close()

	res, err := http.Post(ts.URL+"/api/session", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var created struct {
		SID string `json:"sid"`
	}
	if err := json.NewDecoder(res.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.SID == "" {
		t.Fatal("missing session id")
	}
	pcCookie := findCookie(t, res.Cookies(), pcCookieName(created.SID))

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/" + created.SID + "/pc"
	conn, _, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Cookie": []string{pcCookie.String()}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	mobileCookie := joinMobileWithApprovalHTTP(t, ts.URL, created.SID, pcCookie, conn, "iPhone")

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/session/"+created.SID+"/clipboard", bytes.NewBufferString(`{"text":"hello pc"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(mobileCookie)
	sendRes, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer sendRes.Body.Close()
	if sendRes.StatusCode != http.StatusAccepted {
		t.Fatalf("send status %d", sendRes.StatusCode)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		_, b, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var msg event
		if err := json.Unmarshal(b, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Type == "clipboard.text" {
			if msg.Text != "hello pc" {
				t.Fatalf("wrong text %q", msg.Text)
			}
			return
		}
	}
}

func readClipboardEvent(t *testing.T, name string, ch chan event) event {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case got := <-ch:
			if got.Type == "clipboard.text" {
				return got
			}
		case <-timer.C:
			t.Fatalf("%s mobile did not receive clipboard event", name)
		}
	}
}

func readImageEvent(t *testing.T, name string, ch chan event) event {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case got := <-ch:
			if got.Type == "clipboard.image" {
				return got
			}
		case <-timer.C:
			t.Fatalf("%s did not receive clipboard image event", name)
		}
	}
}

func readEncryptedEvent(t *testing.T, name string, ch chan event) event {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case got := <-ch:
			if got.Type == "clipboard.encrypted" {
				return got
			}
		case <-timer.C:
			t.Fatalf("%s did not receive encrypted clipboard event", name)
		}
	}
}

func readDevicesEvent(t *testing.T, name string, ch chan event) event {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case got := <-ch:
			if got.Type == "devices" {
				return got
			}
		case <-timer.C:
			t.Fatalf("%s did not receive devices event", name)
		}
	}
}

func readSessionNameEvent(t *testing.T, name string, ch chan event, want string) event {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case got := <-ch:
			if got.Type == "session" && got.Name == want {
				return got
			}
		case <-timer.C:
			t.Fatalf("%s did not receive session name %q", name, want)
		}
	}
}

func readLinkEvent(t *testing.T, name string, ch chan event) event {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case got := <-ch:
			if got.Type == "link" {
				return got
			}
		case <-timer.C:
			t.Fatalf("%s did not receive link event", name)
		}
	}
}

func readClipboardWS(t *testing.T, conn *websocket.Conn) event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		_, b, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var msg event
		if err := json.Unmarshal(b, &msg); err != nil {
			t.Fatal(err)
		}
		if msg.Type == "clipboard.text" {
			return msg
		}
	}
}

func joinMobileWithApprovalHTTP(t *testing.T, baseURL, sid string, approverCookie *http.Cookie, events *websocket.Conn, device string) *http.Cookie {
	t.Helper()
	joinReq, err := http.NewRequest(http.MethodPost, baseURL+"/api/session/"+sid+"/join", strings.NewReader(`{"device":`+strconv.Quote(device)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	joinReq.Header.Set("Content-Type", "application/json")
	joinRes, err := http.DefaultClient.Do(joinReq)
	if err != nil {
		t.Fatal(err)
	}
	defer joinRes.Body.Close()
	if joinRes.StatusCode != http.StatusAccepted {
		t.Fatalf("join status %d", joinRes.StatusCode)
	}
	pendingCookie := findCookie(t, joinRes.Cookies(), pendingJoinCookieName(sid))
	request := readJoinRequestWS(t, events)

	approveReq, err := http.NewRequest(http.MethodPost, baseURL+"/api/session/"+sid+"/joins/"+request.ID+"/approve", nil)
	if err != nil {
		t.Fatal(err)
	}
	approveReq.AddCookie(approverCookie)
	approveRes, err := http.DefaultClient.Do(approveReq)
	if err != nil {
		t.Fatal(err)
	}
	defer approveRes.Body.Close()
	if approveRes.StatusCode != http.StatusOK {
		t.Fatalf("approve status %d", approveRes.StatusCode)
	}

	claimReq, err := http.NewRequest(http.MethodPost, baseURL+"/api/session/"+sid+"/join", strings.NewReader(`{"device":`+strconv.Quote(device)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	claimReq.Header.Set("Content-Type", "application/json")
	claimReq.AddCookie(pendingCookie)
	claimRes, err := http.DefaultClient.Do(claimReq)
	if err != nil {
		t.Fatal(err)
	}
	defer claimRes.Body.Close()
	if claimRes.StatusCode != http.StatusOK {
		t.Fatalf("claim status %d", claimRes.StatusCode)
	}
	return findCookie(t, claimRes.Cookies(), mobileCookieName(sid))
}

func readJoinRequestWS(t *testing.T, conn *websocket.Conn) joinRequestView {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for {
		_, b, err := conn.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var msg event
		if err := json.Unmarshal(b, &msg); err != nil {
			t.Fatal(err)
		}
		if len(msg.JoinRequests) > 0 {
			return msg.JoinRequests[0]
		}
	}
}

func findCookie(t *testing.T, cookies []*http.Cookie, name string) *http.Cookie {
	t.Helper()
	for _, c := range cookies {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("missing cookie %s", name)
	return nil
}
