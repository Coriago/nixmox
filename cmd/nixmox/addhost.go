package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"nixmox/internal/addhost"
)

func newAddHostCommand() *cobra.Command {
	var repo, input string
	var write bool
	cmd := &cobra.Command{
		Use: "add-host", Short: "Generate a host configuration from the configured addHost template",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if cfg.AddHost == nil {
				return fmt.Errorf("add_host is not configured")
			}
			var reader io.Reader = cmd.InOrStdin()
			if input != "-" {
				f, err := os.Open(input)
				if err != nil {
					return err
				}
				defer f.Close()
				reader = f
			}
			var in addhost.Input
			decoder := json.NewDecoder(reader)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&in); err != nil {
				return err
			}
			var extra any
			if err := decoder.Decode(&extra); err != io.EOF {
				return fmt.Errorf("input must contain exactly one JSON object")
			}
			result, err := addhost.Generate(*cfg.AddHost, in)
			if err != nil {
				return err
			}
			if write {
				if repo == "" {
					return fmt.Errorf("--repo is required with --write")
				}
				if err := addhost.Write(repo, result); err != nil {
					return err
				}
			}
			return json.NewEncoder(cmd.OutOrStdout()).Encode(result)
		},
	}
	cmd.Flags().StringVar(&input, "input", "-", "JSON form values and installer observations; - reads stdin")
	cmd.Flags().StringVar(&repo, "repo", "", "Isolated repository checkout to receive the configuration")
	cmd.Flags().BoolVar(&write, "write", false, "Create the configuration without overwriting existing files (default: preview JSON)")
	return cmd
}
