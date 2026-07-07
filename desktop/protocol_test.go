package main

import (
	"context"
	"testing"
)

func TestDeviceEventStoresPendingJoinForManualApproval(t *testing.T) {
	oldNotify := showNotificationFunc
	defer func() { showNotificationFunc = oldNotify }()
	var notices []string
	showNotificationFunc = func(message string) {
		notices = append(notices, message)
	}

	app := &desktopApp{pendingSeen: make(map[string]bool)}
	app.handleEvent(context.Background(), relayEvent{
		Type: "devices",
		Devices: []deviceView{
			{Name: "Windows PC", Connected: true, Active: true},
		},
		JoinRequests: []joinRequestView{
			{ID: "pending1", Name: "Phone"},
		},
	})

	s := app.snapshot()
	if got := s["devices"].(int); got != 1 {
		t.Fatalf("devices = %d", got)
	}
	joins := s["joinList"].([]joinRequestView)
	if len(joins) != 1 || joins[0].ID != "pending1" {
		t.Fatalf("join list = %#v", joins)
	}
	if len(notices) != 1 || notices[0] != "New device wants to connect" {
		t.Fatalf("notices = %#v", notices)
	}
}
