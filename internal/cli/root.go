// Package cli implements Cobra commands. Except serve, every operation uses HTTP.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"nixmox/internal/app"
	"nixmox/internal/client"
	"nixmox/internal/domain"
	"nixmox/internal/runtime"
)

type options struct {
	endpoint, config string
	json             bool
}

type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func Execute(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	command := NewCommand()
	command.SetArgs(args)
	command.SetOut(stdout)
	command.SetErr(stderr)
	if err := command.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(stderr, err)
		return ExitCode(err)
	}
	return 0
}

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var failure *exitError
	if errors.As(err, &failure) {
		return failure.code
	}
	return 2 // Cobra argument/flag validation, before an operation starts.
}

func NewCommand() *cobra.Command {
	o := &options{}
	root := &cobra.Command{Use: "nixmox", Short: "Manage Nixmox nodes through their HTTP API", SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().StringVar(&o.endpoint, "endpoint", "", "Console HTTP origin (then NIXMOX_ENDPOINT, then local config)")
	root.PersistentFlags().StringVar(&o.config, "config", "", "Runtime YAML file (default: "+runtime.ConfigPath+")")
	root.PersistentFlags().BoolVar(&o.json, "json", false, "Emit machine-readable JSON; never prompt")
	serve := &cobra.Command{Use: "serve", Short: "Run the local daemon", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		config, err := runtime.LoadFile(o.config, cmd.Flags())
		if err != nil {
			return &exitError{2, err}
		}
		return operationError(app.Serve(cmd.Context(), config))
	}}
	runtime.AddFlags(serve.Flags())
	root.AddCommand(serve, o.identityCommand(), o.discoveryCommands(), o.clusterCommand(), o.hostCommands(), o.networkCommands(), o.taskCommands(), o.receiptCommand())
	return root
}

func (o *options) api() (*client.Client, error) {
	// An explicit remote endpoint must not depend on unrelated local daemon config.
	endpoint := runtime.Endpoint(o.endpoint, os.Getenv("NIXMOX_ENDPOINT"), nil)
	if o.endpoint == "" && os.Getenv("NIXMOX_ENDPOINT") == "" {
		config, err := runtime.LoadFile(o.config, nil)
		if err != nil {
			return nil, &exitError{2, err}
		}
		endpoint = runtime.Endpoint("", "", &config)
	}
	api, err := client.New(endpoint, nil)
	if err != nil {
		return nil, &exitError{2, err}
	}
	return api, nil
}

func (o *options) print(cmd *cobra.Command, value any, err error) error {
	if err != nil {
		return operationError(err)
	}
	encoder := json.NewEncoder(cmd.OutOrStdout())
	if !o.json {
		encoder.SetIndent("", "  ")
	}
	return operationError(encoder.Encode(value))
}

func operationError(err error) error {
	if err == nil {
		return nil
	}
	code := 3
	var detail *domain.Error
	if errors.As(err, &detail) {
		switch detail.Code {
		case "INVALID_INPUT", "NOT_FOUND", "VERSION_CONFLICT", "IDEMPOTENCY_CONFLICT":
			code = 2
		}
	}
	return &exitError{code, err}
}
