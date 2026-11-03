package cli

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"nixmox/internal/credentials"
)

func secretInput(cmd *cobra.Command, stdin bool) ([]byte, error) {
	if stdin {
		value, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), credentials.MaxSecretSize+1))
		if err != nil {
			clear(value)
			return nil, err
		}
		if len(value) > credentials.MaxSecretSize {
			clear(value)
			return nil, errors.New("secret exceeds 64 KiB")
		}
		return value, nil
	}
	file, ok := cmd.InOrStdin().(interface{ Fd() uintptr })
	if !ok || !term.IsTerminal(file.Fd()) {
		return nil, errors.New("hidden prompt requires a terminal; use --stdin for credentials")
	}
	fmt.Fprint(cmd.ErrOrStderr(), "Secret: ")
	value, err := term.ReadPassword(file.Fd())
	fmt.Fprintln(cmd.ErrOrStderr())
	if err != nil {
		clear(value)
		return nil, err
	}
	if len(value) > credentials.MaxSecretSize {
		clear(value)
		return nil, errors.New("secret exceeds 64 KiB")
	}
	return value, err
}

func (c console) addSecrets(root *cobra.Command) {
	put := &cobra.Command{
		Use:   "put",
		Short: "Encrypt and store a credential",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			value, err := secretInput(cmd, boolean(cmd, "stdin"))
			if err != nil {
				return err
			}
			defer clear(value)
			return c.call(cmd, http.MethodPost, "/credentials", map[string]any{"name": flag(cmd, "name"), "type": flag(cmd, "type"), "value": value})
		},
	}
	required(put, "name", "Credential name")
	required(put, "type", "Credential type")
	put.Flags().Bool("stdin", false, "Read secret bytes from stdin instead of a hidden prompt")
	child(root, "credential").AddCommand(put)
	connect := &cobra.Command{
		Use:   "connect",
		Short: "Connect local Wi-Fi (password is not a command argument)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			value, err := secretInput(cmd, false)
			if err != nil {
				return err
			}
			defer clear(value)
			return c.call(cmd, http.MethodPost, "/network/wifi/connect", map[string]any{"ssid": flag(cmd, "ssid"), "interface": flag(cmd, "interface"), "password": string(value)})
		},
	}
	required(connect, "ssid", "Network SSID")
	connect.Flags().String("interface", "", "Network interface")
	child(child(root, "network"), "wifi").AddCommand(connect)
}
