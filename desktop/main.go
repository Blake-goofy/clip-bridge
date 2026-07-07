package main

import (
	"context"
	"log"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	app, err := newDesktopApp(ctx, stop)
	if err != nil {
		log.Fatal(err)
	}
	go app.connectLoop(ctx)

	uiURL, err := startControlServer(ctx, app)
	if err != nil {
		log.Fatal(err)
	}
	if err := openBrowser(uiURL); err != nil {
		log.Printf("open control window: %v", err)
	}

	<-ctx.Done()
}
