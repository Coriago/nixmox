package main

import (
	"github.com/maxence-charriere/go-app/v10/pkg/app"
	"nixmox/internal/ui/progress/browser"
)

func main() { browser.Register(); app.RunWhenOnBrowser() }
