//go:build !js

package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"nixmox/internal/runtime"
	"nixmox/internal/ui/progress"
	"nixmox/internal/ui/progress/browser"
	"nixmox/internal/ui/progress/terminal"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := command().ExecuteContext(ctx)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func command() *cobra.Command {
	root := &cobra.Command{Use: "progress-preview", Short: "Frozen mock UI reference", SilenceUsage: true, SilenceErrors: true}
	root.AddCommand(&cobra.Command{Use: "tui", Short: "Open the terminal reference", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if !term.IsTerminal(os.Stdin.Fd()) || !term.IsTerminal(os.Stdout.Fd()) {
			return fmt.Errorf("tui requires terminal input and output")
		}
		_, err := tea.NewProgram(terminal.New(progress.NewDemo(), os.Stdout), tea.WithContext(cmd.Context()), tea.WithAltScreen()).Run()
		return err
	}})
	var port int
	var assets string
	web := &cobra.Command{Use: "web", Short: "Serve the browser reference on loopback", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if port < 1 || port > 65535 {
			return fmt.Errorf("port must be in 1..65535")
		}
		if assets == "" {
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			assets = filepath.Join(filepath.Dir(exe), "../share/nixmox-progress")
		}
		return browser.Serve(cmd.Context(), port, assets)
	}}
	web.Flags().IntVar(&port, "port", runtime.Defaults().HTTP.Port, "Loopback HTTP port")
	web.Flags().StringVar(&assets, "assets", "", "Directory containing app.wasm and matching wasm_exec.js")
	root.AddCommand(web)
	return root
}
