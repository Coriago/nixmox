//go:build !js

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
	"nixmox/internal/ui/progress"
	"nixmox/internal/ui/progress/browser"
	"nixmox/internal/ui/progress/terminal"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("usage: progress-preview tui | web [--port 63170] [--assets DIR]")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch os.Args[1] {
	case "tui":
		if len(os.Args) != 2 {
			return fmt.Errorf("tui takes no arguments")
		}
		if !term.IsTerminal(os.Stdin.Fd()) || !term.IsTerminal(os.Stdout.Fd()) {
			return fmt.Errorf("tui requires terminal input and output")
		}
		_, err := tea.NewProgram(terminal.New(progress.NewDemo(), os.Stdout), tea.WithContext(ctx), tea.WithAltScreen()).Run()
		return err
	case "web":
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		flags := flag.NewFlagSet("web", flag.ContinueOnError)
		port := flags.Int("port", 63170, "loopback HTTP port")
		assets := flags.String("assets", filepath.Join(filepath.Dir(exe), "../share/nixmox-progress"), "directory containing app.wasm and matching wasm_exec.js")
		if err := flags.Parse(os.Args[2:]); err != nil {
			return err
		}
		if flags.NArg() != 0 || *port < 1 || *port > 65535 {
			return fmt.Errorf("invalid web arguments or port")
		}
		return browser.Serve(ctx, *port, *assets)
	default:
		return fmt.Errorf("unknown preview mode %q", os.Args[1])
	}
}
