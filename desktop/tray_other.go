//go:build !windows

package main

import "context"

func runTray(ctx context.Context, app *desktopApp) {
	app.openUI()
	<-ctx.Done()
}
