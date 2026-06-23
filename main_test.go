package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestFirstMobileWinsAndCookieReuse(t *testing.T) {
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
	if _, _, err := h.joinMobile(sid, "wrong", "Other"); err == nil {
		t.Fatal("second mobile should be rejected")
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
	if err := a.hub.relayClipboard(sid, mobileToken, "hello"); err != errPCGone {
		t.Fatalf("expected errPCGone, got %v", err)
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
	var pcCookie *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == pcCookieName(created.SID) {
			pcCookie = c
		}
	}
	if pcCookie == nil {
		t.Fatal("missing pc cookie")
	}

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
	var mobileCookie *http.Cookie
	for _, c := range joinRes.Cookies() {
		if c.Name == mobileCookieName(created.SID) {
			mobileCookie = c
		}
	}
	if mobileCookie == nil {
		t.Fatal("missing mobile cookie")
	}

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
