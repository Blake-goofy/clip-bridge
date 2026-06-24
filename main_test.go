package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestSessionIDUsesWordCode(t *testing.T) {
	h := newHub()
	sid, _, err := h.createSession()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[a-z]+(-[a-z]+){5}$`).MatchString(sid) {
		t.Fatalf("session id is not a six-word code: %q", sid)
	}
}

func TestSessionWordListIsComplete(t *testing.T) {
	if len(sessionWords) != 256 {
		t.Fatalf("got %d session words, want 256", len(sessionWords))
	}
	seen := make(map[string]bool, len(sessionWords))
	for _, word := range sessionWords {
		if word == "" {
			t.Fatal("session word list contains an empty word")
		}
		if seen[word] {
			t.Fatalf("session word list contains duplicate %q", word)
		}
		seen[word] = true
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
		select {
		case got := <-ch:
			if got.Type != "clipboard.text" || got.Text != "hello all" {
				t.Fatalf("%s got wrong event %#v", name, got)
			}
		default:
			t.Fatalf("%s mobile did not receive clipboard event", name)
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

	var latest event
	for i := 0; i < 4; i++ {
		latest = <-pcCh
	}
	if latest.Type != "devices" {
		t.Fatalf("got event type %q, want devices", latest.Type)
	}
	connected := 0
	for _, d := range latest.Devices {
		if d.Connected {
			connected++
		}
	}
	if connected != 2 {
		t.Fatalf("connected devices = %d, want 2: %#v", connected, latest.Devices)
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
	var deviceID string
	for id := range a.hub.sessions[sid].devices {
		deviceID = id
	}
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

func TestIndexUsesNeutralClipboardUI(t *testing.T) {
	for _, old := range []string{"Windows", "iPhone", "Safari", "No phone", "Connected to PC", "Send Clipboard", "<title>Clip Bridge</title>", "Waiting.", "receiver", "sender", "New Session", "position: fixed", "border-radius: 24px", "box-shadow", "notif-overlay", "qrWrap.classList.toggle(\"hidden\", event.connected)"} {
		if strings.Contains(indexHTML, old) {
			t.Fatalf("index still contains platform-specific or removed UI text %q", old)
		}
	}
	for _, want := range []string{"<title>ClipBridge</title>", "<h1>ClipBridge</h1>", "rotatingWord", "Send clipboard", "Source code", "localStorage", "/resume", "devicePane", "copyConnectURL", "deviceCount", "/disconnect", "pcActions", "pcMessages", "mobileMessages", "navigator.clipboard.writeText(text || \"\")"} {
		if !strings.Contains(indexHTML, want) {
			t.Fatalf("index is missing chat UI marker %q", want)
		}
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

	joinRes, err := http.Post(ts.URL+"/api/session/"+created.SID+"/join", "application/json", strings.NewReader(`{"device":"Device"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer joinRes.Body.Close()
	if joinRes.StatusCode != http.StatusOK {
		t.Fatalf("join status %d", joinRes.StatusCode)
	}
	mobileCookie := findCookie(t, joinRes.Cookies(), mobileCookieName(created.SID))

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

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, b, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var msg event
	if err := json.Unmarshal(b, &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Type != "clipboard.text" || msg.Text != "hello mobile" {
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

	joinRes, err := http.Post(ts.URL+"/api/session/"+created.SID+"/join", "application/json", strings.NewReader(`{"device":"iPhone"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer joinRes.Body.Close()
	if joinRes.StatusCode != http.StatusOK {
		t.Fatalf("join status %d", joinRes.StatusCode)
	}
	mobileCookie := findCookie(t, joinRes.Cookies(), mobileCookieName(created.SID))

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
