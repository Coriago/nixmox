//go:build js && wasm

package main

import (
	"github.com/maxence-charriere/go-app/v11/pkg/app"

	"nixmox/internal/ui/views/browser"
)

func main() {
	browser.Register()
	app.RunWhenOnBrowser()
}
