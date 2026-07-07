package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	closeLog := initLogging()
	defer closeLog()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	app, err := newDesktopApp(ctx, stop)
	if err != nil {
		log.Fatal(err)
	}
	go app.runRelay(ctx)

	bridge, err := startControlServer(ctx, app)
	if err != nil {
		log.Fatal(err)
	}
	app.setUIURL(bridge.BaseURL, bridge.Token)

	go func() {
		timer := time.NewTimer(1200 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
			// ponytail: open once on startup until tray visibility is proven reliable across Windows notification settings.
			app.openUI()
		}
	}()

	runTray(ctx, app)
}
