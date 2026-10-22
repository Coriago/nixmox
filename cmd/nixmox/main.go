package main

import (
	"context"
	"nixmox/internal/app"
	"nixmox/internal/console"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := console.Run(ctx, os.Args[1:], os.Stdout, os.Stderr, app.Serve)
	stop()
	os.Exit(code)
}
